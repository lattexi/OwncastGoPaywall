package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// SRSHooksHandler handles SRS webhook callbacks
type SRSHooksHandler struct {
	pgStore *storage.PostgresStore
}

// NewSRSHooksHandler creates a new SRS hooks handler
func NewSRSHooksHandler(pgStore *storage.PostgresStore) *SRSHooksHandler {
	return &SRSHooksHandler{
		pgStore: pgStore,
	}
}

// srsCallbackRequest represents the JSON body SRS sends for callbacks
type srsCallbackRequest struct {
	Action   string `json:"action"`
	ClientID string `json:"client_id"`
	IP       string `json:"ip"`
	Vhost    string `json:"vhost"`
	App      string `json:"app"`
	Stream   string `json:"stream"`
	Param    string `json:"param"`
}

// srsCallbackResponse is the response format SRS expects
type srsCallbackResponse struct {
	Code int `json:"code"`
}

// OnPublish handles the on_publish callback from SRS
// SRS sends this when a streamer connects and starts publishing
// Returns {"code":0} to allow, {"code":1} to deny
func (h *SRSHooksHandler) OnPublish(w http.ResponseWriter, r *http.Request) {
	var req srsCallbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error().Err(err).Msg("Failed to decode SRS on_publish callback")
		writeJSON(w, http.StatusOK, srsCallbackResponse{Code: 1})
		return
	}

	log.Info().
		Str("action", req.Action).
		Str("app", req.App).
		Str("stream", req.Stream).
		Str("ip", req.IP).
		Msg("SRS on_publish callback")

	ctx := r.Context()
	streamKey := req.Stream

	// Look up stream by stream key
	stream, err := h.pgStore.GetStreamByStreamKey(ctx, streamKey)
	if err != nil {
		log.Error().Err(err).Str("stream_key", streamKey).Msg("Failed to look up stream")
		writeJSON(w, http.StatusOK, srsCallbackResponse{Code: 1})
		return
	}

	if stream == nil {
		log.Warn().Str("stream_key", streamKey).Msg("Unknown stream key - denying publish")
		writeJSON(w, http.StatusOK, srsCallbackResponse{Code: 1})
		return
	}

	// Stream key is valid - mark as publishing
	if err := h.pgStore.UpdateStreamPublishing(ctx, streamKey, true); err != nil {
		log.Error().Err(err).Str("stream_key", streamKey).Msg("Failed to update publishing status")
	}

	log.Info().
		Str("slug", stream.Slug).
		Str("stream_key", streamKey[:8]+"...").
		Msg("Stream publish allowed")

	writeJSON(w, http.StatusOK, srsCallbackResponse{Code: 0})
}

// OnUnpublish handles the on_unpublish callback from SRS
// SRS sends this when a streamer disconnects
func (h *SRSHooksHandler) OnUnpublish(w http.ResponseWriter, r *http.Request) {
	var req srsCallbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error().Err(err).Msg("Failed to decode SRS on_unpublish callback")
		writeJSON(w, http.StatusOK, srsCallbackResponse{Code: 0})
		return
	}

	log.Info().
		Str("action", req.Action).
		Str("app", req.App).
		Str("stream", req.Stream).
		Msg("SRS on_unpublish callback")

	ctx := r.Context()
	streamKey := req.Stream

	// Mark stream as not publishing
	if err := h.pgStore.UpdateStreamPublishing(ctx, streamKey, false); err != nil {
		log.Error().Err(err).Str("stream_key", streamKey).Msg("Failed to update publishing status")
	}

	writeJSON(w, http.StatusOK, srsCallbackResponse{Code: 0})
}
