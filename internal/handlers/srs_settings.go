package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/middleware"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/srs"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// SRSSettingsHandler handles video settings for SRS transcoding
type SRSSettingsHandler struct {
	cfg       *config.Config
	pgStore   *storage.PostgresStore
	srsConfig *srs.ConfigGenerator
	sessionMw *middleware.AdminSessionMiddleware
}

// NewSRSSettingsHandler creates a new SRS settings handler
func NewSRSSettingsHandler(cfg *config.Config, pgStore *storage.PostgresStore, srsConfig *srs.ConfigGenerator, sessionMw *middleware.AdminSessionMiddleware) *SRSSettingsHandler {
	return &SRSSettingsHandler{
		cfg:       cfg,
		pgStore:   pgStore,
		srsConfig: srsConfig,
		sessionMw: sessionMw,
	}
}

// videoSettingsResponse matches the format the admin UI expects
type videoSettingsResponse struct {
	VideoSettings videoSettings `json:"videoSettings"`
}

type videoSettings struct {
	VideoQualityVariants []videoVariant `json:"videoQualityVariants"`
	LatencyLevel         int           `json:"latencyLevel"`
}

type videoVariant struct {
	Name             string `json:"name,omitempty"`
	VideoBitrate     int    `json:"videoBitrate"`
	AudioBitrate     int    `json:"audioBitrate,omitempty"`
	Framerate        int    `json:"framerate"`
	CPUUsageLevel    int    `json:"cpuUsageLevel"`
	VideoPassthrough bool   `json:"videoPassthrough"`
	AudioPassthrough bool   `json:"audioPassthrough"`
}

// presetResolutions maps preset names to video dimensions
var presetResolutions = map[string][2]int{
	"1080p": {1920, 1080},
	"720p":  {1280, 720},
	"480p":  {854, 480},
	"360p":  {640, 360},
}

// cpuLevelToPreset maps CPU usage level to FFmpeg preset
var cpuLevelToPreset = map[int]string{
	1: "ultrafast",
	2: "faster",
	3: "medium",
	4: "slow",
	5: "veryslow",
}

// GetVideoSettings returns current video settings for a stream
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

	// Parse transcode config from DB
	variants, err := stream.GetTranscodeVariants()
	if err != nil {
		log.Error().Err(err).Str("stream_id", id.String()).Msg("Failed to parse transcode config")
		writeJSONError(w, http.StatusInternalServerError, "Failed to read settings")
		return
	}

	// Convert to UI format
	var uiVariants []videoVariant
	for _, v := range variants {
		cpuLevel := 3 // default balanced
		for level, preset := range cpuLevelToPreset {
			if preset == v.VPreset {
				cpuLevel = level
				break
			}
		}

		uiVariants = append(uiVariants, videoVariant{
			Name:             v.Name,
			VideoBitrate:     v.VBitrate,
			AudioBitrate:     v.ABitrate,
			Framerate:        v.VFps,
			CPUUsageLevel:    cpuLevel,
			VideoPassthrough: v.Passthrough,
			AudioPassthrough: true,
		})
	}

	if len(uiVariants) == 0 {
		// Default to 720p
		uiVariants = []videoVariant{
			{Name: "720p", VideoBitrate: 2500, Framerate: 30, CPUUsageLevel: 3, VideoPassthrough: false, AudioPassthrough: true},
		}
	}

	writeJSON(w, http.StatusOK, videoSettingsResponse{
		VideoSettings: videoSettings{
			VideoQualityVariants: uiVariants,
			LatencyLevel:         2, // SRS doesn't have latency levels, default to "normal"
		},
	})
}

// UpdateVideoSettings updates video settings for a stream
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

	// Parse request body (same format as old Owncast handler)
	var req struct {
		Variants     []videoVariant `json:"variants"`
		LatencyLevel *int           `json:"latencyLevel,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Convert UI variants to transcode config
	var transcodeVariants []models.TranscodeVariant
	for _, v := range req.Variants {
		preset := cpuLevelToPreset[v.CPUUsageLevel]
		if preset == "" {
			preset = "medium"
		}

		// Look up resolution from name
		width, height := 0, 0
		if res, ok := presetResolutions[v.Name]; ok {
			width = res[0]
			height = res[1]
		}

		tv := models.TranscodeVariant{
			Name:        v.Name,
			VBitrate:    v.VideoBitrate,
			VWidth:      width,
			VHeight:     height,
			VFps:        v.Framerate,
			VPreset:     preset,
			ABitrate:    v.AudioBitrate,
			Passthrough: v.VideoPassthrough,
		}
		transcodeVariants = append(transcodeVariants, tv)
	}

	// Marshal and save to DB
	configJSON, err := json.Marshal(transcodeVariants)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to encode settings")
		return
	}

	if err := h.pgStore.UpdateTranscodeConfig(ctx, id, configJSON); err != nil {
		log.Error().Err(err).Str("stream_id", id.String()).Msg("Failed to save transcode config")
		writeJSONError(w, http.StatusInternalServerError, "Failed to save settings")
		return
	}

	// Regenerate SRS config and reload
	if err := h.srsConfig.GenerateAndReload(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to reload SRS config")
		// Don't fail - settings are saved, SRS will pick up on next reload
	}

	log.Info().Str("stream_id", id.String()).Msg("SRS video settings updated")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Video settings updated",
	})
}
