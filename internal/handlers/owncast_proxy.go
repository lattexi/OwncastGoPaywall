package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/middleware"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// OwncastAPIHandler handles Owncast API interactions
type OwncastAPIHandler struct {
	cfg       *config.Config
	pgStore   *storage.PostgresStore
	redis     *storage.RedisStore
	sessionMw *middleware.AdminSessionMiddleware
	client    *http.Client
}

// NewOwncastProxyHandler creates a new Owncast API handler
func NewOwncastProxyHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore, sessionMw *middleware.AdminSessionMiddleware) *OwncastAPIHandler {
	return &OwncastAPIHandler{
		cfg:       cfg,
		pgStore:   pgStore,
		redis:     redis,
		sessionMw: sessionMw,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// OwncastVideoVariant represents a video quality variant
type OwncastVideoVariant struct {
	Name             string `json:"name,omitempty"`
	VideoBitrate     int    `json:"videoBitrate"`
	AudioBitrate     int    `json:"audioBitrate,omitempty"`
	Framerate        int    `json:"framerate"`
	CPUUsageLevel    int    `json:"cpuUsageLevel"`
	VideoPassthrough bool   `json:"videoPassthrough"`
	AudioPassthrough bool   `json:"audioPassthrough"`
}

// OwncastVideoSettings represents video settings from Owncast
type OwncastVideoSettings struct {
	VideoQualityVariants []OwncastVideoVariant `json:"videoQualityVariants"`
	LatencyLevel         int                   `json:"latencyLevel"`
}

// OwncastServerConfig represents the server config response
type OwncastServerConfig struct {
	VideoSettings OwncastVideoSettings `json:"videoSettings"`
}

// GetVideoSettings returns current video settings for a stream's Owncast instance
func (h *OwncastAPIHandler) GetVideoSettings(w http.ResponseWriter, r *http.Request) {
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

	if stream.ContainerStatus != models.ContainerStatusRunning {
		writeJSONError(w, http.StatusServiceUnavailable, "Container is not running")
		return
	}

	// Fetch config from Owncast
	config, err := h.fetchOwncastConfig(stream.OwncastURL)
	if err != nil {
		log.Error().Err(err).Str("stream_id", id.String()).Msg("Failed to fetch Owncast config")
		writeJSONError(w, http.StatusBadGateway, "Failed to fetch Owncast settings")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"videoSettings": config.VideoSettings,
	})
}

// UpdateVideoSettings updates video settings for a stream's Owncast instance
func (h *OwncastAPIHandler) UpdateVideoSettings(w http.ResponseWriter, r *http.Request) {
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

	if stream.ContainerStatus != models.ContainerStatusRunning {
		writeJSONError(w, http.StatusServiceUnavailable, "Container is not running")
		return
	}

	// Parse request body
	var req struct {
		Variants     []OwncastVideoVariant `json:"variants"`
		LatencyLevel *int                  `json:"latencyLevel,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Update video variants
	if len(req.Variants) > 0 {
		err = h.updateOwncastVideoVariants(stream.OwncastURL, req.Variants)
		if err != nil {
			log.Error().Err(err).Str("stream_id", id.String()).Msg("Failed to update video variants")
			writeJSONError(w, http.StatusBadGateway, "Failed to update video settings")
			return
		}
	}

	// Update latency level if provided
	if req.LatencyLevel != nil {
		err = h.updateOwncastLatency(stream.OwncastURL, *req.LatencyLevel)
		if err != nil {
			log.Error().Err(err).Str("stream_id", id.String()).Msg("Failed to update latency level")
			writeJSONError(w, http.StatusBadGateway, "Failed to update latency settings")
			return
		}
	}

	log.Info().Str("stream_id", id.String()).Msg("Owncast video settings updated")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Video settings updated",
	})
}

// fetchOwncastConfig fetches the server config from Owncast
func (h *OwncastAPIHandler) fetchOwncastConfig(owncastURL string) (*OwncastServerConfig, error) {
	url := owncastURL + "/api/admin/serverconfig"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	h.addBasicAuth(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("owncast returned status %d: %s", resp.StatusCode, string(body))
	}

	var config OwncastServerConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// updateOwncastVideoVariants updates video quality variants
func (h *OwncastAPIHandler) updateOwncastVideoVariants(owncastURL string, variants []OwncastVideoVariant) error {
	url := owncastURL + "/api/admin/config/video/streamoutputvariants"

	payload := map[string]interface{}{
		"value": variants,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	h.addBasicAuth(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("owncast returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// updateOwncastLatency updates the latency level
func (h *OwncastAPIHandler) updateOwncastLatency(owncastURL string, level int) error {
	url := owncastURL + "/api/admin/config/video/streamlatencylevel"

	payload := map[string]interface{}{
		"value": level,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	h.addBasicAuth(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("owncast returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// addBasicAuth adds basic auth header to request
func (h *OwncastAPIHandler) addBasicAuth(req *http.Request) {
	auth := base64.StdEncoding.EncodeToString([]byte("admin:" + h.cfg.OwncastAdminPassword))
	req.Header.Set("Authorization", "Basic "+auth)
}
