package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/srs"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// SRSSettingsHandler handles video settings stored in the DB
type SRSSettingsHandler struct {
	pgStore    *storage.PostgresStore
	srsManager *srs.Manager
}

// NewSRSSettingsHandler creates a new SRS settings handler
func NewSRSSettingsHandler(pgStore *storage.PostgresStore, srsManager *srs.Manager) *SRSSettingsHandler {
	return &SRSSettingsHandler{
		pgStore:    pgStore,
		srsManager: srsManager,
	}
}

// GetVideoSettings returns the transcode config for a stream
func (h *SRSSettingsHandler) GetVideoSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	stream, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil || stream == nil {
		writeJSONError(w, http.StatusNotFound, "Stream not found")
		return
	}

	// Parse transcode config
	var variants []models.TranscodeVariant
	if len(stream.TranscodeConfig) > 0 {
		json.Unmarshal(stream.TranscodeConfig, &variants)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"variants": variants,
	})
}

// UpdateVideoSettings updates the transcode config for a stream
func (h *SRSSettingsHandler) UpdateVideoSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid stream ID")
		return
	}

	stream, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil || stream == nil {
		writeJSONError(w, http.StatusNotFound, "Stream not found")
		return
	}

	var req struct {
		Variants []models.TranscodeVariant `json:"variants"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	configJSON, err := json.Marshal(req.Variants)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to encode config")
		return
	}

	if err := h.pgStore.UpdateTranscodeConfig(ctx, id, configJSON); err != nil {
		log.Error().Err(err).Str("stream_id", id.String()).Msg("Failed to update transcode config")
		writeJSONError(w, http.StatusInternalServerError, "Failed to update settings")
		return
	}

	// Regenerate SRS config and reload
	if err := h.srsManager.WriteAndReload(); err != nil {
		log.Error().Err(err).Msg("Failed to reload SRS config")
	}

	log.Info().Str("stream_id", id.String()).Msg("Video settings updated")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Video settings updated",
	})
}
