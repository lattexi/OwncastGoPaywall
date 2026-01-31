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
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// AdminPageHandler handles admin page rendering
type AdminPageHandler struct {
	cfg         *config.Config
	pgStore     *storage.PostgresStore
	redis       *storage.RedisStore
	templates   *template.Template
	sessionMw   *middleware.AdminSessionMiddleware
	dockerMgr   *docker.Manager
}

// NewAdminPageHandler creates a new admin page handler
func NewAdminPageHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore, templateDir string, sessionMw *middleware.AdminSessionMiddleware, dockerMgr *docker.Manager) (*AdminPageHandler, error) {
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
		dockerMgr: dockerMgr,
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
	TotalStreams       int
	ActiveViewers      int64
	TotalRevenueEuros  float64
	CompletedPayments  int
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

	// Get stats
	streams, _ := h.pgStore.ListStreams(ctx)
	
	var totalPayments, completedPayments int
	var totalRevenue int
	var activeViewers int64 = 0
	var recentPayments []PaymentWithStream

	// Create stream title map
	streamTitles := make(map[uuid.UUID]string)
	var liveStreams []*models.Stream

	for _, stream := range streams {
		streamTitles[stream.ID] = stream.Title
		
		if stream.Status == models.StreamStatusLive {
			liveStreams = append(liveStreams, stream)
		}

		count, _ := h.redis.CountActiveSessions(ctx, stream.ID)
		activeViewers += count

		payments, _ := h.pgStore.ListPaymentsByStream(ctx, stream.ID)
		for _, p := range payments {
			totalPayments++
			if p.Status == models.PaymentStatusCompleted {
				completedPayments++
				totalRevenue += p.AmountCents

				// Collect recent payments
				if len(recentPayments) < 10 {
					recentPayments = append(recentPayments, PaymentWithStream{
						Payment:     p,
						StreamTitle: streamTitles[p.StreamID],
						AmountEuros: float64(p.AmountCents) / 100,
					})
				}
			}
		}
	}

	data := struct {
		AdminBaseData
		Stats          DashboardStats
		LiveStreams    []*models.Stream
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
			TotalRevenueEuros: float64(totalRevenue) / 100,
			CompletedPayments: completedPayments,
		},
		LiveStreams:    liveStreams,
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

	// Generate container-related fields
	streamKey, err := docker.GenerateStreamKey()
	if err != nil {
		log.Error().Err(err).Msg("Failed to generate stream key")
		h.renderStreamFormError(w, session, nil, false, "Failed to generate stream key.")
		return
	}

	rtmpPort, err := h.pgStore.GetNextAvailablePort(ctx, h.cfg.RTMPPortStart)
	if err != nil {
		log.Error().Err(err).Msg("Failed to allocate RTMP port")
		h.renderStreamFormError(w, session, nil, false, "Failed to allocate RTMP port.")
		return
	}

	containerName := docker.ContainerName(slug)
	owncastURL := docker.GetInternalURL(containerName)

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
		OwncastURL:      owncastURL,
		MaxViewers:      maxViewers,
		CreatedAt:       time.Now(),
		StreamKey:       streamKey,
		RTMPPort:        rtmpPort,
		ContainerName:   containerName,
		ContainerStatus: models.ContainerStatusStopped,
	}

	if err := h.pgStore.CreateStream(ctx, stream); err != nil {
		log.Error().Err(err).Msg("Failed to create stream")
		h.renderStreamFormError(w, session, nil, false, "Failed to create stream.")
		return
	}

	log.Info().
		Str("slug", slug).
		Str("container", containerName).
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

	// Update (note: owncast_url, stream_key, rtmp_port, container_name are immutable)
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

	// Get stream to find container name
	stream, _ := h.pgStore.GetStreamByID(ctx, id)

	// Remove container and volume if they exist
	if stream != nil && stream.Slug != "" && h.dockerMgr != nil {
		if err := h.dockerMgr.RemoveContainer(ctx, stream.Slug); err != nil {
			log.Warn().Err(err).Str("slug", stream.Slug).Msg("Failed to remove container")
		}
	}

	if err := h.pgStore.DeleteStream(ctx, id); err != nil {
		log.Error().Err(err).Msg("Failed to delete stream")
	} else {
		log.Info().Str("id", id.String()).Str("admin", session.Username).Msg("Stream deleted")
	}

	http.Redirect(w, r, "/admin/streams", http.StatusFound)
}

// StartContainer starts the Owncast container for a stream
func (h *AdminPageHandler) StartContainer(w http.ResponseWriter, r *http.Request) {
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

	// Update status to starting
	h.pgStore.UpdateContainerStatus(ctx, id, models.ContainerStatusStarting)

	// Start container
	if h.dockerMgr != nil {
		err = h.dockerMgr.CreateAndStartContainer(ctx, stream.Slug, stream.StreamKey, stream.RTMPPort)
		if err != nil {
			log.Error().Err(err).Str("slug", stream.Slug).Msg("Failed to start container")
			h.pgStore.UpdateContainerStatus(ctx, id, models.ContainerStatusError)
		} else {
			log.Info().
				Str("slug", stream.Slug).
				Str("container", stream.ContainerName).
				Int("rtmp_port", stream.RTMPPort).
				Str("admin", session.Username).
				Msg("Container started")
			h.pgStore.UpdateContainerStatus(ctx, id, models.ContainerStatusRunning)
		}
	} else {
		log.Warn().Msg("Docker manager not available")
		h.pgStore.UpdateContainerStatus(ctx, id, models.ContainerStatusError)
	}

	// Redirect back
	referer := r.Header.Get("Referer")
	if referer != "" {
		http.Redirect(w, r, referer, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/streams", http.StatusFound)
}

// StopContainer stops the Owncast container for a stream
func (h *AdminPageHandler) StopContainer(w http.ResponseWriter, r *http.Request) {
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

	// Update status to stopping
	h.pgStore.UpdateContainerStatus(ctx, id, models.ContainerStatusStopping)

	// Stop container
	if h.dockerMgr != nil {
		err = h.dockerMgr.StopContainer(ctx, stream.ContainerName)
		if err != nil {
			log.Error().Err(err).Str("container", stream.ContainerName).Msg("Failed to stop container")
			h.pgStore.UpdateContainerStatus(ctx, id, models.ContainerStatusError)
		} else {
			log.Info().
				Str("slug", stream.Slug).
				Str("container", stream.ContainerName).
				Str("admin", session.Username).
				Msg("Container stopped")
			h.pgStore.UpdateContainerStatus(ctx, id, models.ContainerStatusStopped)
		}
	} else {
		h.pgStore.UpdateContainerStatus(ctx, id, models.ContainerStatusStopped)
	}

	// Redirect back
	referer := r.Header.Get("Referer")
	if referer != "" {
		http.Redirect(w, r, referer, http.StatusFound)
		return
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

// render renders a template
func (h *AdminPageHandler) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.templates.ExecuteTemplate(w, name, data)
	if err != nil {
		log.Error().Err(err).Str("template", name).Msg("Failed to render admin template")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
