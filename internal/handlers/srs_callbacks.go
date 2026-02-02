package handlers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// SRSCallbackRequest represents the JSON payload SRS sends to HTTP callbacks
type SRSCallbackRequest struct {
	Action   string `json:"action"`
	ClientID string `json:"client_id"`
	IP       string `json:"ip"`
	Vhost    string `json:"vhost"`
	App      string `json:"app"`
	Stream   string `json:"stream"`
	Param    string `json:"param"` // Query string like "?key=abc123"
}

// SRSCallbackHandler handles SRS HTTP callback requests
type SRSCallbackHandler struct {
	pgStore *storage.PostgresStore
}

// NewSRSCallbackHandler creates a new SRS callback handler
func NewSRSCallbackHandler(pgStore *storage.PostgresStore) *SRSCallbackHandler {
	return &SRSCallbackHandler{
		pgStore: pgStore,
	}
}

// OnPublish handles the on_publish callback from SRS
// Called when a streamer starts publishing to validate the stream key
// POST /api/srs/on_publish
func (h *SRSCallbackHandler) OnPublish(w http.ResponseWriter, r *http.Request) {
	var req SRSCallbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error().Err(err).Msg("Failed to decode SRS on_publish request")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	log.Info().
		Str("action", req.Action).
		Str("stream", req.Stream).
		Str("app", req.App).
		Str("ip", req.IP).
		Str("client_id", req.ClientID).
		Msg("SRS on_publish callback received")

	// Extract stream key from param (format: "?key=abc123")
	streamKey := extractParam(req.Param, "key")
	if streamKey == "" {
		log.Warn().
			Str("stream", req.Stream).
			Str("param", req.Param).
			Msg("No stream key provided in on_publish")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]int{"code": 1})
		return
	}

	// Find stream by slug (SRS sends the stream name as the slug)
	stream, err := h.pgStore.GetStreamBySlug(r.Context(), req.Stream)
	if err != nil {
		log.Error().Err(err).Str("stream", req.Stream).Msg("Failed to get stream")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]int{"code": 1})
		return
	}

	if stream == nil {
		log.Warn().Str("stream", req.Stream).Msg("Stream not found")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]int{"code": 1})
		return
	}

	// Validate stream key
	if stream.StreamKey != streamKey {
		log.Warn().
			Str("stream", req.Stream).
			Str("provided_key", streamKey[:min(8, len(streamKey))]+"...").
			Msg("Invalid stream key")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]int{"code": 1})
		return
	}

	log.Info().
		Str("stream", req.Stream).
		Str("slug", stream.Slug).
		Msg("Stream key validated, allowing publish")

	// Success - allow the stream
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]int{"code": 0})
}

// OnUnpublish handles the on_unpublish callback from SRS
// Called when a streamer stops publishing
// POST /api/srs/on_unpublish
func (h *SRSCallbackHandler) OnUnpublish(w http.ResponseWriter, r *http.Request) {
	var req SRSCallbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error().Err(err).Msg("Failed to decode SRS on_unpublish request")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	log.Info().
		Str("action", req.Action).
		Str("stream", req.Stream).
		Str("app", req.App).
		Str("ip", req.IP).
		Msg("SRS on_unpublish callback received")

	// Just acknowledge - no action needed
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]int{"code": 0})
}

// extractParam extracts a query parameter value from a param string
// param format: "?key=value&other=foo" or "key=value&other=foo"
func extractParam(param, key string) string {
	// Remove leading "?" if present
	param = strings.TrimPrefix(param, "?")
	if param == "" {
		return ""
	}

	values, err := url.ParseQuery(param)
	if err != nil {
		return ""
	}

	return values.Get(key)
}
