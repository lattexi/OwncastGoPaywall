package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// AdminHandler handles admin API endpoints
type AdminHandler struct {
	cfg     *config.Config
	pgStore *storage.PostgresStore
	redis   *storage.RedisStore
}

// NewAdminHandler creates a new admin handler
func NewAdminHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore) *AdminHandler {
	return &AdminHandler{
		cfg:     cfg,
		pgStore: pgStore,
		redis:   redis,
	}
}

// CreateStream creates a new stream
// POST /admin/streams
func (h *AdminHandler) CreateStream(w http.ResponseWriter, r *http.Request) {
	var req models.CreateStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate required fields
	if req.Slug == "" {
		writeJSONError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if req.Title == "" {
		writeJSONError(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.PriceCents < 0 {
		writeJSONError(w, http.StatusBadRequest, "price_cents must be non-negative")
		return
	}

	ctx := r.Context()

	// Check if slug is already taken
	existing, _ := h.pgStore.GetStreamBySlug(ctx, req.Slug)
	if existing != nil {
		writeJSONError(w, http.StatusConflict, "A stream with this slug already exists")
		return
	}

	// Create stream - container fields will be set by admin page handler
	stream := &models.Stream{
		ID:              uuid.New(),
		Slug:            req.Slug,
		Title:           req.Title,
		Description:     req.Description,
		PriceCents:      req.PriceCents,
		StartTime:       req.StartTime,
		EndTime:         req.EndTime,
		Status:          models.StreamStatusScheduled,
		MaxViewers:      req.MaxViewers,
		CreatedAt:       time.Now(),
		ContainerStatus: models.ContainerStatusStopped,
	}

	if err := h.pgStore.CreateStream(ctx, stream); err != nil {
		log.Error().Err(err).Msg("Failed to create stream")
		writeJSONError(w, http.StatusInternalServerError, "Failed to create stream")
		return
	}

	log.Info().
		Str("id", stream.ID.String()).
		Str("slug", stream.Slug).
		Msg("Stream created")

	writeJSON(w, http.StatusCreated, stream)
}

// GetStream retrieves a stream by ID
// GET /admin/streams/{id}
func (h *AdminHandler) GetStream(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	ctx := r.Context()
	stream, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get stream")
		writeJSONError(w, http.StatusInternalServerError, "Failed to get stream")
		return
	}
	if stream == nil {
		writeJSONError(w, http.StatusNotFound, "Stream not found")
		return
	}

	// Include internal fields for admin (override json:"-")
	response := map[string]interface{}{
		"id":               stream.ID,
		"slug":             stream.Slug,
		"title":            stream.Title,
		"description":      stream.Description,
		"price_cents":      stream.PriceCents,
		"start_time":       stream.StartTime,
		"end_time":         stream.EndTime,
		"status":           stream.Status,
		"owncast_url":      stream.OwncastURL,
		"max_viewers":      stream.MaxViewers,
		"created_at":       stream.CreatedAt,
		"stream_key":       stream.StreamKey,
		"rtmp_port":        stream.RTMPPort,
		"container_name":   stream.ContainerName,
		"container_status": stream.ContainerStatus,
	}

	writeJSON(w, http.StatusOK, response)
}

// UpdateStream updates a stream
// PUT /admin/streams/{id}
func (h *AdminHandler) UpdateStream(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	var req models.UpdateStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	ctx := r.Context()

	// Check stream exists
	existing, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil || existing == nil {
		writeJSONError(w, http.StatusNotFound, "Stream not found")
		return
	}

	if err := h.pgStore.UpdateStream(ctx, id, &req); err != nil {
		log.Error().Err(err).Msg("Failed to update stream")
		writeJSONError(w, http.StatusInternalServerError, "Failed to update stream")
		return
	}

	log.Info().Str("id", id.String()).Msg("Stream updated")

	// Return updated stream
	stream, _ := h.pgStore.GetStreamByID(ctx, id)
	writeJSON(w, http.StatusOK, stream)
}

// UpdateStreamStatus updates only the stream status
// PATCH /admin/streams/{id}/status
func (h *AdminHandler) UpdateStreamStatus(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate status
	status := models.StreamStatus(req.Status)
	if status != models.StreamStatusScheduled && status != models.StreamStatusLive && status != models.StreamStatusEnded {
		writeJSONError(w, http.StatusBadRequest, "Invalid status. Must be: scheduled, live, or ended")
		return
	}

	ctx := r.Context()

	if err := h.pgStore.UpdateStreamStatus(ctx, id, status); err != nil {
		log.Error().Err(err).Msg("Failed to update stream status")
		writeJSONError(w, http.StatusInternalServerError, "Failed to update stream status")
		return
	}

	log.Info().
		Str("id", id.String()).
		Str("status", req.Status).
		Msg("Stream status updated")

	writeJSON(w, http.StatusOK, models.APISuccess{Success: true, Message: "Status updated"})
}

// DeleteStream deletes a stream
// DELETE /admin/streams/{id}
func (h *AdminHandler) DeleteStream(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	ctx := r.Context()

	if err := h.pgStore.DeleteStream(ctx, id); err != nil {
		log.Error().Err(err).Msg("Failed to delete stream")
		writeJSONError(w, http.StatusInternalServerError, "Failed to delete stream")
		return
	}

	log.Info().Str("id", id.String()).Msg("Stream deleted")

	writeJSON(w, http.StatusOK, models.APISuccess{Success: true, Message: "Stream deleted"})
}

// ListStreams lists all streams
// GET /admin/streams
func (h *AdminHandler) ListStreams(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	streams, err := h.pgStore.ListStreams(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list streams")
		writeJSONError(w, http.StatusInternalServerError, "Failed to list streams")
		return
	}

	// Include internal fields for admin
	response := make([]map[string]interface{}, len(streams))
	for i, stream := range streams {
		response[i] = map[string]interface{}{
			"id":               stream.ID,
			"slug":             stream.Slug,
			"title":            stream.Title,
			"description":      stream.Description,
			"price_cents":      stream.PriceCents,
			"start_time":       stream.StartTime,
			"end_time":         stream.EndTime,
			"status":           stream.Status,
			"owncast_url":      stream.OwncastURL,
			"max_viewers":      stream.MaxViewers,
			"created_at":       stream.CreatedAt,
			"stream_key":       stream.StreamKey,
			"rtmp_port":        stream.RTMPPort,
			"container_name":   stream.ContainerName,
			"container_status": stream.ContainerStatus,
		}
	}

	writeJSON(w, http.StatusOK, response)
}

// GetViewerCount returns the current viewer count for a stream
// GET /admin/streams/{id}/viewers
func (h *AdminHandler) GetViewerCount(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	ctx := r.Context()

	// Get active session count from Redis
	count, err := h.redis.CountActiveSessions(ctx, id)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get viewer count")
		writeJSONError(w, http.StatusInternalServerError, "Failed to get viewer count")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"stream_id":    id,
		"viewer_count": count,
	})
}

// ListPayments lists all payments for a stream
// GET /admin/streams/{id}/payments
func (h *AdminHandler) ListPayments(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	ctx := r.Context()

	payments, err := h.pgStore.ListPaymentsByStream(ctx, id)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list payments")
		writeJSONError(w, http.StatusInternalServerError, "Failed to list payments")
		return
	}

	// Sanitize payment data (hide full tokens)
	response := make([]map[string]interface{}, len(payments))
	for i, p := range payments {
		tokenPreview := ""
		if p.AccessToken != "" && len(p.AccessToken) > 8 {
			tokenPreview = p.AccessToken[:8] + "..."
		}

		response[i] = map[string]interface{}{
			"id":                     p.ID,
			"stream_id":              p.StreamID,
			"email":                  p.Email,
			"amount_cents":           p.AmountCents,
			"status":                 p.Status,
			"paytrail_ref":           p.PaytrailRef,
			"paytrail_transaction_id": p.PaytrailTransactionID,
			"token_preview":          tokenPreview,
			"token_expiry":           p.TokenExpiry,
			"created_at":             p.CreatedAt,
		}
	}

	writeJSON(w, http.StatusOK, response)
}

// GetStats returns overall stats
// GET /admin/stats
func (h *AdminHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	streams, err := h.pgStore.ListStreams(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to get stats")
		return
	}

	var totalPayments, completedPayments, totalRevenue int
	var activeViewers int64 = 0

	for _, stream := range streams {
		payments, _ := h.pgStore.ListPaymentsByStream(ctx, stream.ID)
		for _, p := range payments {
			totalPayments++
			if p.Status == models.PaymentStatusCompleted {
				completedPayments++
				totalRevenue += p.AmountCents
			}
		}

		count, _ := h.redis.CountActiveSessions(ctx, stream.ID)
		activeViewers += count
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_streams":      len(streams),
		"total_payments":     totalPayments,
		"completed_payments": completedPayments,
		"total_revenue_cents": totalRevenue,
		"total_revenue_euros": float64(totalRevenue) / 100,
		"active_viewers":     activeViewers,
	})
}

// --- Whitelist Management ---

// ListWhitelist returns all whitelisted emails for a stream
// GET /admin/streams/{id}/whitelist
func (h *AdminHandler) ListWhitelist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	ctx := r.Context()

	entries, err := h.pgStore.GetWhitelistByStream(ctx, id)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get whitelist")
		writeJSONError(w, http.StatusInternalServerError, "Failed to get whitelist")
		return
	}

	if entries == nil {
		entries = []*models.WhitelistEntry{}
	}

	writeJSON(w, http.StatusOK, entries)
}

// AddToWhitelist adds an email to a stream's whitelist
// POST /admin/streams/{id}/whitelist
func (h *AdminHandler) AddToWhitelist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	var req struct {
		Email string `json:"email"`
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Email == "" {
		writeJSONError(w, http.StatusBadRequest, "Email is required")
		return
	}

	ctx := r.Context()

	// Verify stream exists
	stream, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil || stream == nil {
		writeJSONError(w, http.StatusNotFound, "Stream not found")
		return
	}

	entry, err := h.pgStore.AddWhitelistEntry(ctx, id, req.Email, req.Notes)
	if err != nil {
		log.Error().Err(err).Msg("Failed to add to whitelist")
		writeJSONError(w, http.StatusInternalServerError, "Failed to add to whitelist")
		return
	}

	log.Info().
		Str("stream_id", id.String()).
		Str("email", req.Email).
		Msg("Email added to whitelist")

	writeJSON(w, http.StatusCreated, entry)
}

// RemoveFromWhitelist removes an email from a stream's whitelist
// DELETE /admin/streams/{id}/whitelist/{email}
func (h *AdminHandler) RemoveFromWhitelist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	email := r.PathValue("email")
	if email == "" {
		writeJSONError(w, http.StatusBadRequest, "Email is required")
		return
	}

	ctx := r.Context()

	err = h.pgStore.RemoveWhitelistEntry(ctx, id, email)
	if err != nil {
		log.Error().Err(err).Msg("Failed to remove from whitelist")
		writeJSONError(w, http.StatusInternalServerError, "Failed to remove from whitelist")
		return
	}

	log.Info().
		Str("stream_id", id.String()).
		Str("email", email).
		Msg("Email removed from whitelist")

	writeJSON(w, http.StatusOK, models.APISuccess{Success: true, Message: "Email removed from whitelist"})
}
