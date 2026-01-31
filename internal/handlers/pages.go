package handlers

import (
	"html/template"
	"net/http"
	"time"

	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/security"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// PageHandler handles page rendering
type PageHandler struct {
	cfg         *config.Config
	pgStore     *storage.PostgresStore
	redis       *storage.RedisStore
	templates   *template.Template
	templateDir string
	urlSigner   *security.URLSigner
}

// NewPageHandler creates a new page handler
func NewPageHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore, templateDir string) (*PageHandler, error) {
	// Parse only the base template initially
	// Child templates are parsed per-request to avoid block conflicts
	templates, err := template.ParseFiles(templateDir + "/base.html")
	if err != nil {
		return nil, err
	}

	return &PageHandler{
		cfg:         cfg,
		pgStore:     pgStore,
		redis:       redis,
		templates:   templates,
		templateDir: templateDir,
		urlSigner:   security.NewURLSigner(cfg.SigningSecret, cfg.SignatureValidity),
	}, nil
}

// BaseData contains common data for all pages
type BaseData struct {
	Title string
	Year  int
}

// HomeData contains data for the home page
type HomeData struct {
	BaseData
	Streams []*models.Stream
}

// StreamData contains data for the stream detail page
type StreamData struct {
	BaseData
	Stream    *models.Stream
	HasAccess bool
}

// WatchData contains data for the watch page
type WatchData struct {
	BaseData
	Stream      *models.Stream
	PlaylistURL string
}

// RecoverData contains data for the recovery page
type RecoverData struct {
	BaseData
	Stream *models.Stream
}

// ErrorData contains data for error pages
type ErrorData struct {
	BaseData
	Code       int
	Message    string
	StreamSlug string
}

// Home renders the home page
func (h *PageHandler) Home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	streams, err := h.pgStore.ListActiveStreams(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list streams")
		h.renderError(w, 500, "Failed to load streams", "")
		return
	}

	data := HomeData{
		BaseData: BaseData{
			Title: "Available Streams",
			Year:  time.Now().Year(),
		},
		Streams: streams,
	}

	h.render(w, "home.html", data)
}

// Stream renders the stream detail/purchase page
func (h *PageHandler) Stream(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		h.renderError(w, 404, "Stream not found", "")
		return
	}

	ctx := r.Context()

	stream, err := h.pgStore.GetStreamBySlug(ctx, slug)
	if err != nil {
		log.Error().Err(err).Str("slug", slug).Msg("Failed to get stream")
		h.renderError(w, 500, "Failed to load stream", "")
		return
	}
	if stream == nil {
		h.renderError(w, 404, "Stream not found", "")
		return
	}

	// Check if user has access
	hasAccess := false
	if cookie, err := r.Cookie("access_token"); err == nil && cookie.Value != "" {
		payment, err := h.pgStore.GetPaymentByAccessToken(ctx, cookie.Value)
		if err == nil && payment != nil && payment.IsTokenValid() && payment.StreamID == stream.ID {
			hasAccess = true
		}
	}

	data := StreamData{
		BaseData: BaseData{
			Title: stream.Title,
			Year:  time.Now().Year(),
		},
		Stream:    stream,
		HasAccess: hasAccess,
	}

	h.render(w, "stream.html", data)
}

// Watch renders the video player page
func (h *PageHandler) Watch(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		h.renderError(w, 404, "Stream not found", "")
		return
	}

	ctx := r.Context()

	stream, err := h.pgStore.GetStreamBySlug(ctx, slug)
	if err != nil || stream == nil {
		h.renderError(w, 404, "Stream not found", "")
		return
	}

	// Get token from query param first, then fall back to cookie
	token := r.URL.Query().Get("token")
	if token == "" {
		cookie, err := r.Cookie("access_token")
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/stream/"+slug, http.StatusFound)
			return
		}
		token = cookie.Value
	} else {
		// Set cookie from query param for future requests
		http.SetCookie(w, &http.Cookie{
			Name:     "access_token",
			Value:    token,
			Path:     "/",
			MaxAge:   86400, // 24 hours
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}

	// Validate token
	payment, err := h.pgStore.GetPaymentByAccessToken(ctx, token)
	if err != nil || payment == nil || !payment.IsTokenValid() {
		// Clear invalid cookie
		http.SetCookie(w, &http.Cookie{
			Name:   "access_token",
			Value:  "",
			Path:   "/",
			MaxAge: -1,
		})
		http.Redirect(w, r, "/stream/"+slug, http.StatusFound)
		return
	}

	// Verify token is for this stream
	if payment.StreamID != stream.ID {
		h.renderError(w, 403, "Access denied. You haven't purchased access to this stream.", slug)
		return
	}

	// Generate signed playlist URL
	playlistPath := "/stream/" + stream.ID.String() + "/hls/stream.m3u8"
	playlistURL := h.cfg.BaseURL + h.urlSigner.SignURL(stream.ID.String(), token, playlistPath)

	data := WatchData{
		BaseData: BaseData{
			Title: stream.Title,
			Year:  time.Now().Year(),
		},
		Stream:      stream,
		PlaylistURL: playlistURL,
	}

	h.render(w, "watch.html", data)
}

// Recover renders the token recovery page
func (h *PageHandler) Recover(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		h.renderError(w, 404, "Stream not found", "")
		return
	}

	ctx := r.Context()

	stream, err := h.pgStore.GetStreamBySlug(ctx, slug)
	if err != nil || stream == nil {
		h.renderError(w, 404, "Stream not found", "")
		return
	}

	data := RecoverData{
		BaseData: BaseData{
			Title: "Recover Access",
			Year:  time.Now().Year(),
		},
		Stream: stream,
	}

	h.render(w, "recover.html", data)
}

// render renders a template
func (h *PageHandler) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Clone base template and parse the specific child template
	// This allows each child template to have its own block definitions
	tmpl, err := h.templates.Clone()
	if err != nil {
		log.Error().Err(err).Str("template", name).Msg("Failed to clone template")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Parse the specific child template
	tmpl, err = tmpl.ParseFiles(h.templateDir + "/" + name)
	if err != nil {
		log.Error().Err(err).Str("template", name).Msg("Failed to parse template")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	err = tmpl.ExecuteTemplate(w, "base.html", data)
	if err != nil {
		log.Error().Err(err).Str("template", name).Msg("Failed to render template")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

// renderError renders an error page
func (h *PageHandler) renderError(w http.ResponseWriter, code int, message, streamSlug string) {
	w.WriteHeader(code)

	data := ErrorData{
		BaseData: BaseData{
			Title: "Error",
			Year:  time.Now().Year(),
		},
		Code:       code,
		Message:    message,
		StreamSlug: streamSlug,
	}

	h.render(w, "error.html", data)
}

// NotFound handles 404 errors
func (h *PageHandler) NotFound(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, 404, "", "")
}
