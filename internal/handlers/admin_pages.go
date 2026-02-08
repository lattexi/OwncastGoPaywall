package handlers

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/docker"
	"github.com/laurikarhu/stream-paywall/internal/middleware"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/srs"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// AdminPageHandler handles admin page rendering
type AdminPageHandler struct {
	cfg       *config.Config
	pgStore   *storage.PostgresStore
	redis     *storage.RedisStore
	templates *template.Template
	sessionMw *middleware.AdminSessionMiddleware
	srsConfig *srs.ConfigGenerator
}

// NewAdminPageHandler creates a new admin page handler
func NewAdminPageHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore, templateDir string, sessionMw *middleware.AdminSessionMiddleware, srsConfig *srs.ConfigGenerator) (*AdminPageHandler, error) {
	// Parse admin templates
	templates, err := template.ParseGlob(templateDir + "/admin/*.html")
	if err != nil {
		return nil, err
	}

	return &AdminPageHandler{
		cfg:       cfg,
		pgStore:   pgStore,
		redis:     redis,
		templates: templates,
		sessionMw: sessionMw,
		srsConfig: srsConfig,
	}, nil
}

// AdminBaseData contains common data for admin pages
type AdminBaseData struct {
	Title      string
	ActivePage string
	ShowNav    bool
	Username   string
	Year       int
}

// --- Login ---

// ShowLogin renders the login page
func (h *AdminPageHandler) ShowLogin(w http.ResponseWriter, r *http.Request) {
	// Check if already logged in
	if cookie, err := r.Cookie(middleware.AdminSessionCookieName); err == nil && cookie.Value != "" {
		session, _ := h.redis.GetAdminSession(r.Context(), cookie.Value)
		if session != nil {
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
	}

	data := struct {
		AdminBaseData
		Error    string
		Username string
	}{
		AdminBaseData: AdminBaseData{
			Title:   "Login",
			ShowNav: false,
			Year:    time.Now().Year(),
		},
	}

	h.render(w, "login.html", data)
}

// ProcessLogin handles login form submission
func (h *AdminPageHandler) ProcessLogin(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	ctx := r.Context()

	// Rate limit check
	clientIP := getClientIP(r)
	allowed, err := h.redis.CheckAdminLoginRateLimit(ctx, username, clientIP)
	if err != nil {
		log.Error().Err(err).Msg("Failed to check login rate limit")
	}
	if !allowed {
		h.renderLoginError(w, "Too many login attempts. Please try again later.", username)
		return
	}

	// Verify credentials
	user, valid := h.pgStore.VerifyAdminPassword(ctx, username, password)
	if !valid {
		log.Warn().Str("username", username).Str("ip", clientIP).Msg("Failed admin login attempt")
		h.renderLoginError(w, "Invalid username or password.", username)
		return
	}

	// Create session
	sessionID, err := h.sessionMw.CreateSession(ctx, user)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create admin session")
		h.renderLoginError(w, "Failed to create session. Please try again.", username)
		return
	}

	// Set cookie
	h.sessionMw.SetSessionCookie(w, r, sessionID)

	log.Info().Str("username", username).Msg("Admin logged in")

	http.Redirect(w, r, "/admin", http.StatusFound)
}

// Logout handles logout
func (h *AdminPageHandler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(middleware.AdminSessionCookieName)
	if err == nil && cookie.Value != "" {
		h.sessionMw.ClearSession(r.Context(), w, cookie.Value)
	}
	http.Redirect(w, r, "/admin/login", http.StatusFound)
}

func (h *AdminPageHandler) renderLoginError(w http.ResponseWriter, errorMsg, username string) {
	data := struct {
		AdminBaseData
		Error    string
		Username string
	}{
		AdminBaseData: AdminBaseData{
			Title:   "Login",
			ShowNav: false,
			Year:    time.Now().Year(),
		},
		Error:    errorMsg,
		Username: username,
	}
	h.render(w, "login.html", data)
}

// --- Dashboard ---

// DashboardStats contains dashboard statistics
type DashboardStats struct {
	TotalStreams      int
	ActiveViewers     int64
	TotalRevenueEuros float64
	CompletedPayments int
}

// PaymentWithStream represents a payment with stream title
type PaymentWithStream struct {
	*models.Payment
	StreamTitle string
	AmountEuros float64
}

// Dashboard renders the admin dashboard
func (h *AdminPageHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	// Get streams (needed for live streams list and viewer count)
	streams, _ := h.pgStore.ListStreams(ctx)

	// Get aggregated payment stats in ONE query
	paymentStats, err := h.pgStore.GetPaymentStatsAggregate(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get payment stats")
		paymentStats = &storage.PaymentStats{}
	}

	// Get recent payments with stream titles in ONE query
	recentPaymentsList, streamTitles, err := h.pgStore.GetRecentCompletedPayments(ctx, 10)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get recent payments")
	}

	// Build recent payments with stream info
	var recentPayments []PaymentWithStream
	for _, p := range recentPaymentsList {
		recentPayments = append(recentPayments, PaymentWithStream{
			Payment:     p,
			StreamTitle: streamTitles[p.StreamID],
			AmountEuros: float64(p.AmountCents) / 100,
		})
	}

	// Get live streams and active viewers
	var liveStreams []*models.Stream
	var activeViewers int64 = 0
	for _, stream := range streams {
		if stream.Status == models.StreamStatusLive {
			liveStreams = append(liveStreams, stream)
		}
		count, _ := h.redis.CountActiveSessions(ctx, stream.ID)
		activeViewers += count
	}

	data := struct {
		AdminBaseData
		Stats          DashboardStats
		LiveStreams     []*models.Stream
		RecentPayments []PaymentWithStream
	}{
		AdminBaseData: AdminBaseData{
			Title:      "Dashboard",
			ActivePage: "dashboard",
			ShowNav:    true,
			Username:   session.Username,
			Year:       time.Now().Year(),
		},
		Stats: DashboardStats{
			TotalStreams:      len(streams),
			ActiveViewers:     activeViewers,
			TotalRevenueEuros: float64(paymentStats.TotalRevenueCents) / 100,
			CompletedPayments: paymentStats.CompletedPayments,
		},
		LiveStreams:     liveStreams,
		RecentPayments: recentPayments,
	}

	h.render(w, "dashboard.html", data)
}

// --- Streams ---

// StreamWithStats extends Stream with additional stats
type StreamWithStats struct {
	*models.Stream
	PriceEuros float64
	RTMPURL    string // Full RTMP URL for OBS configuration
}

// ListStreams renders the streams list
func (h *AdminPageHandler) ListStreams(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	streams, err := h.pgStore.ListStreams(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list streams")
		http.Error(w, "Failed to load streams", http.StatusInternalServerError)
		return
	}

	// Add price in euros and RTMP URL
	var streamsWithStats []StreamWithStats
	for _, s := range streams {
		streamsWithStats = append(streamsWithStats, StreamWithStats{
			Stream:     s,
			PriceEuros: float64(s.PriceCents) / 100,
			RTMPURL:    docker.GetRTMPURL(h.cfg.RTMPPublicHost, s.RTMPPort),
		})
	}

	data := struct {
		AdminBaseData
		Streams []StreamWithStats
	}{
		AdminBaseData: AdminBaseData{
			Title:      "Streams",
			ActivePage: "streams",
			ShowNav:    true,
			Username:   session.Username,
			Year:       time.Now().Year(),
		},
		Streams: streamsWithStats,
	}

	h.render(w, "streams.html", data)
}

// NewStreamForm renders the new stream form
func (h *AdminPageHandler) NewStreamForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	data := struct {
		AdminBaseData
		Stream *models.Stream
		IsEdit bool
		Error  string
	}{
		AdminBaseData: AdminBaseData{
			Title:      "New Stream",
			ActivePage: "streams",
			ShowNav:    true,
			Username:   session.Username,
			Year:       time.Now().Year(),
		},
		IsEdit: false,
	}

	h.render(w, "stream_form.html", data)
}

// CreateStream handles stream creation
func (h *AdminPageHandler) CreateStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	// Parse form
	slug := strings.TrimSpace(r.FormValue("slug"))
	title := strings.TrimSpace(r.FormValue("title"))
	description := strings.TrimSpace(r.FormValue("description"))
	priceStr := r.FormValue("price")
	maxViewersStr := r.FormValue("max_viewers")
	startTimeStr := r.FormValue("start_time")
	endTimeStr := r.FormValue("end_time")

	// Validate
	if slug == "" || title == "" {
		h.renderStreamFormError(w, session, nil, false, "Slug and title are required.")
		return
	}

	// Parse price
	priceFloat, err := strconv.ParseFloat(priceStr, 64)
	if err != nil || priceFloat < 0 {
		h.renderStreamFormError(w, session, nil, false, "Invalid price.")
		return
	}
	priceCents := int(priceFloat * 100)

	// Parse max viewers
	maxViewers := 0
	if maxViewersStr != "" {
		maxViewers, _ = strconv.Atoi(maxViewersStr)
	}

	// Parse times
	var startTime, endTime *time.Time
	if startTimeStr != "" {
		t, err := time.Parse("2006-01-02T15:04", startTimeStr)
		if err == nil {
			startTime = &t
		}
	}
	if endTimeStr != "" {
		t, err := time.Parse("2006-01-02T15:04", endTimeStr)
		if err == nil {
			endTime = &t
		}
	}

	// Check slug uniqueness
	existing, _ := h.pgStore.GetStreamBySlug(ctx, slug)
	if existing != nil {
		h.renderStreamFormError(w, session, nil, false, "A stream with this slug already exists.")
		return
	}

	// Generate stream key
	streamKey, err := docker.GenerateStreamKey()
	if err != nil {
		log.Error().Err(err).Msg("Failed to generate stream key")
		h.renderStreamFormError(w, session, nil, false, "Failed to generate stream key.")
		return
	}

	// All streams use the single SRS RTMP port
	rtmpPort := h.cfg.RTMPPortStart

	// Create stream
	stream := &models.Stream{
		ID:              uuid.New(),
		Slug:            slug,
		Title:           title,
		Description:     description,
		PriceCents:      priceCents,
		StartTime:       startTime,
		EndTime:         endTime,
		Status:          models.StreamStatusScheduled,
		MaxViewers:      maxViewers,
		CreatedAt:       time.Now(),
		StreamKey:       streamKey,
		RTMPPort:        rtmpPort,
		ContainerStatus: models.ContainerStatusStopped,
		IsPublishing:    false,
	}

	if err := h.pgStore.CreateStream(ctx, stream); err != nil {
		log.Error().Err(err).Msg("Failed to create stream")
		h.renderStreamFormError(w, session, nil, false, "Failed to create stream.")
		return
	}

	// Regenerate SRS config to include webhook validation for new stream key
	if h.srsConfig != nil {
		if err := h.srsConfig.GenerateAndReload(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to reload SRS config after stream creation")
		}
	}

	log.Info().
		Str("slug", slug).
		Int("rtmp_port", rtmpPort).
		Str("admin", session.Username).
		Msg("Stream created")

	http.Redirect(w, r, "/admin/streams", http.StatusFound)
}

// EditStreamForm renders the edit stream form
func (h *AdminPageHandler) EditStreamForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Redirect(w, r, "/admin/streams", http.StatusFound)
		return
	}

	stream, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil || stream == nil {
		http.Redirect(w, r, "/admin/streams", http.StatusFound)
		return
	}

	data := struct {
		AdminBaseData
		Stream   *StreamWithStats
		IsEdit   bool
		Error    string
		AdminKey string
	}{
		AdminBaseData: AdminBaseData{
			Title:      "Edit Stream",
			ActivePage: "streams",
			ShowNav:    true,
			Username:   session.Username,
			Year:       time.Now().Year(),
		},
		Stream: &StreamWithStats{
			Stream:     stream,
			PriceEuros: float64(stream.PriceCents) / 100,
			RTMPURL:    docker.GetRTMPURL(h.cfg.RTMPPublicHost, stream.RTMPPort),
		},
		IsEdit:   true,
		AdminKey: h.cfg.AdminAPIKey,
	}

	h.render(w, "stream_form.html", data)
}

// UpdateStream handles stream update
func (h *AdminPageHandler) UpdateStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Redirect(w, r, "/admin/streams", http.StatusFound)
		return
	}

	stream, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil || stream == nil {
		http.Redirect(w, r, "/admin/streams", http.StatusFound)
		return
	}

	// Parse form
	title := strings.TrimSpace(r.FormValue("title"))
	description := strings.TrimSpace(r.FormValue("description"))
	priceStr := r.FormValue("price")
	maxViewersStr := r.FormValue("max_viewers")
	startTimeStr := r.FormValue("start_time")
	endTimeStr := r.FormValue("end_time")
	statusStr := r.FormValue("status")

	// Validate
	if title == "" {
		h.renderStreamFormError(w, session, &StreamWithStats{Stream: stream, PriceEuros: float64(stream.PriceCents) / 100}, true, "Title is required.")
		return
	}

	// Parse price
	priceFloat, err := strconv.ParseFloat(priceStr, 64)
	if err != nil || priceFloat < 0 {
		h.renderStreamFormError(w, session, &StreamWithStats{Stream: stream, PriceEuros: float64(stream.PriceCents) / 100}, true, "Invalid price.")
		return
	}
	priceCents := int(priceFloat * 100)

	// Parse max viewers
	maxViewers := 0
	if maxViewersStr != "" {
		maxViewers, _ = strconv.Atoi(maxViewersStr)
	}

	// Parse times
	var startTime, endTime *time.Time
	if startTimeStr != "" {
		t, err := time.Parse("2006-01-02T15:04", startTimeStr)
		if err == nil {
			startTime = &t
		}
	}
	if endTimeStr != "" {
		t, err := time.Parse("2006-01-02T15:04", endTimeStr)
		if err == nil {
			endTime = &t
		}
	}

	// Parse status
	status := models.StreamStatus(statusStr)
	if status != models.StreamStatusScheduled && status != models.StreamStatusLive && status != models.StreamStatusEnded {
		status = stream.Status
	}

	updates := &models.UpdateStreamRequest{
		Title:       &title,
		Description: &description,
		PriceCents:  &priceCents,
		StartTime:   startTime,
		EndTime:     endTime,
		Status:      &status,
		MaxViewers:  &maxViewers,
	}

	if err := h.pgStore.UpdateStream(ctx, id, updates); err != nil {
		log.Error().Err(err).Msg("Failed to update stream")
		h.renderStreamFormError(w, session, &StreamWithStats{Stream: stream, PriceEuros: float64(stream.PriceCents) / 100}, true, "Failed to update stream.")
		return
	}

	log.Info().Str("id", id.String()).Str("admin", session.Username).Msg("Stream updated")

	http.Redirect(w, r, "/admin/streams", http.StatusFound)
}

// UpdateStreamStatus handles status update
func (h *AdminPageHandler) UpdateStreamStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Redirect(w, r, "/admin/streams", http.StatusFound)
		return
	}

	statusStr := r.FormValue("status")
	status := models.StreamStatus(statusStr)

	if err := h.pgStore.UpdateStreamStatus(ctx, id, status); err != nil {
		log.Error().Err(err).Msg("Failed to update stream status")
	} else {
		log.Info().Str("id", id.String()).Str("status", statusStr).Str("admin", session.Username).Msg("Stream status updated")
	}

	// Redirect back to referrer or streams page
	referer := r.Header.Get("Referer")
	if referer != "" {
		http.Redirect(w, r, referer, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/streams", http.StatusFound)
}

// DeleteStream handles stream deletion
func (h *AdminPageHandler) DeleteStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Redirect(w, r, "/admin/streams", http.StatusFound)
		return
	}

	if err := h.pgStore.DeleteStream(ctx, id); err != nil {
		log.Error().Err(err).Msg("Failed to delete stream")
	} else {
		log.Info().Str("id", id.String()).Str("admin", session.Username).Msg("Stream deleted")
	}

	// Regenerate SRS config after stream deletion
	if h.srsConfig != nil {
		if err := h.srsConfig.GenerateAndReload(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to reload SRS config after stream deletion")
		}
	}

	http.Redirect(w, r, "/admin/streams", http.StatusFound)
}

func (h *AdminPageHandler) renderStreamFormError(w http.ResponseWriter, session *storage.AdminSession, stream *StreamWithStats, isEdit bool, errorMsg string) {
	data := struct {
		AdminBaseData
		Stream *StreamWithStats
		IsEdit bool
		Error  string
	}{
		AdminBaseData: AdminBaseData{
			Title:      "Stream",
			ActivePage: "streams",
			ShowNav:    true,
			Username:   session.Username,
			Year:       time.Now().Year(),
		},
		Stream: stream,
		IsEdit: isEdit,
		Error:  errorMsg,
	}
	h.render(w, "stream_form.html", data)
}

// --- Payments ---

// PaymentView represents a payment for display
type PaymentView struct {
	*models.Payment
	AmountEuros  float64
	TokenPreview string
}

// StreamPayments renders the payments list for a stream
func (h *AdminPageHandler) StreamPayments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Redirect(w, r, "/admin/streams", http.StatusFound)
		return
	}

	stream, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil || stream == nil {
		http.Redirect(w, r, "/admin/streams", http.StatusFound)
		return
	}

	payments, err := h.pgStore.ListPaymentsByStream(ctx, id)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list payments")
		payments = []*models.Payment{}
	}

	// Convert to view models
	var paymentViews []PaymentView
	var totalRevenue float64
	completedCount := 0

	for _, p := range payments {
		tokenPreview := ""
		if p.AccessToken != "" && len(p.AccessToken) > 8 {
			tokenPreview = p.AccessToken[:8] + "..."
		}

		paymentViews = append(paymentViews, PaymentView{
			Payment:      p,
			AmountEuros:  float64(p.AmountCents) / 100,
			TokenPreview: tokenPreview,
		})

		if p.Status == models.PaymentStatusCompleted {
			completedCount++
			totalRevenue += float64(p.AmountCents) / 100
		}
	}

	data := struct {
		AdminBaseData
		Stream            *models.Stream
		Payments          []PaymentView
		TotalPayments     int
		CompletedPayments int
		TotalRevenue      float64
	}{
		AdminBaseData: AdminBaseData{
			Title:      "Payments - " + stream.Title,
			ActivePage: "streams",
			ShowNav:    true,
			Username:   session.Username,
			Year:       time.Now().Year(),
		},
		Stream:            stream,
		Payments:          paymentViews,
		TotalPayments:     len(payments),
		CompletedPayments: completedCount,
		TotalRevenue:      totalRevenue,
	}

	h.render(w, "payments.html", data)
}

// --- API Endpoints for Admin ---

// GetViewerCountAPI returns viewer count as JSON
func (h *AdminPageHandler) GetViewerCountAPI(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	count, err := h.redis.CountActiveSessions(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to get viewer count")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"stream_id":    id,
		"viewer_count": count,
	})
}

// --- Metrics Page ---

// MetricsPage renders the metrics dashboard
func (h *AdminPageHandler) MetricsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := middleware.GetAdminSession(ctx)

	data := struct {
		AdminBaseData
	}{
		AdminBaseData: AdminBaseData{
			Title:      "System Metrics",
			ActivePage: "metrics",
			ShowNav:    true,
			Username:   session.Username,
			Year:       time.Now().Year(),
		},
	}

	h.render(w, "metrics.html", data)
}

// render renders a template
func (h *AdminPageHandler) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.templates.ExecuteTemplate(w, name, data)
	if err != nil {
		log.Error().Err(err).Str("template", name).Msg("Failed to render admin template")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
