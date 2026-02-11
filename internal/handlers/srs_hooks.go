package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// SRSHookHandler handles SRS webhook callbacks
type SRSHookHandler struct {
	pgStore *storage.PostgresStore
}

// NewSRSHookHandler creates a new SRS hook handler
func NewSRSHookHandler(pgStore *storage.PostgresStore) *SRSHookHandler {
	return &SRSHookHandler{pgStore: pgStore}
}

// srsHookRequest represents the JSON body SRS sends for on_publish/on_unpublish
type srsHookRequest struct {
	Action string `json:"action"`
	IP     string `json:"ip"`
	Vhost  string `json:"vhost"`
	App    string `json:"app"`
	Stream string `json:"stream"`
	Param  string `json:"param"` // e.g., "?key=abc123"
}

// srsHookResponse is the response SRS expects
type srsHookResponse struct {
	Code int `json:"code"` // 0 = allow, 1 = deny
}

// OnPublish handles SRS on_publish webhook
// SRS calls this when a streamer connects via RTMP
func (h *SRSHookHandler) OnPublish(w http.ResponseWriter, r *http.Request) {
	var req srsHookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error().Err(err).Msg("Failed to decode SRS on_publish request")
		writeHookResponse(w, 1)
		return
	}

	log.Info().
		Str("app", req.App).
		Str("stream", req.Stream).
		Str("param", req.Param).
		Str("ip", req.IP).
		Msg("SRS on_publish webhook received")

	// Extract stream key from the stream name or param
	// OBS sends: rtmp://host:port/live?key=STREAM_KEY
	// SRS parses this as app="live", param="?key=STREAM_KEY"
	streamKey := extractStreamKey(req.Stream, req.Param)
	if streamKey == "" {
		log.Warn().Str("stream", req.Stream).Str("param", req.Param).Msg("No stream key found")
		writeHookResponse(w, 1)
		return
	}

	// Check if this is a transcoded variant stream (e.g., key_720p, key_480p)
	// These are published by FFmpeg internally and should be allowed without DB lookup
	if isTranscodeVariant(streamKey) {
		log.Info().Str("stream", streamKey).Str("ip", req.IP).Msg("Allowing transcoded variant stream")
		writeHookResponse(w, 0)
		return
	}

	// Look up stream by stream key
	ctx := r.Context()
	stream, err := h.pgStore.GetStreamByStreamKey(ctx, streamKey)
	if err != nil {
		log.Error().Err(err).Str("key", streamKey[:8]+"...").Msg("Failed to look up stream key")
		writeHookResponse(w, 1)
		return
	}

	if stream == nil {
		log.Warn().Str("key", streamKey[:8]+"...").Msg("Invalid stream key - rejecting connection")
		writeHookResponse(w, 1)
		return
	}

	// Valid stream key - set publishing flag
	if err := h.pgStore.SetPublishing(ctx, streamKey, true); err != nil {
		log.Error().Err(err).Str("slug", stream.Slug).Msg("Failed to set publishing status")
	}

	log.Info().
		Str("slug", stream.Slug).
		Str("ip", req.IP).
		Msg("Stream publishing started")

	writeHookResponse(w, 0)
}

// OnUnpublish handles SRS on_unpublish webhook
// SRS calls this when a streamer disconnects
func (h *SRSHookHandler) OnUnpublish(w http.ResponseWriter, r *http.Request) {
	var req srsHookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error().Err(err).Msg("Failed to decode SRS on_unpublish request")
		writeHookResponse(w, 0)
		return
	}

	log.Info().
		Str("app", req.App).
		Str("stream", req.Stream).
		Str("param", req.Param).
		Msg("SRS on_unpublish webhook received")

	streamKey := extractStreamKey(req.Stream, req.Param)
	if streamKey == "" {
		writeHookResponse(w, 0)
		return
	}

	// Ignore unpublish for transcoded variant streams
	if isTranscodeVariant(streamKey) {
		writeHookResponse(w, 0)
		return
	}

	ctx := r.Context()
	if err := h.pgStore.SetPublishing(ctx, streamKey, false); err != nil {
		log.Error().Err(err).Msg("Failed to clear publishing status")
	} else {
		log.Info().Str("key", streamKey[:8]+"...").Msg("Stream publishing stopped")
	}

	writeHookResponse(w, 0)
}

// extractStreamKey extracts the stream key from SRS webhook data
// The key can come as the stream name itself, or as a query parameter
func extractStreamKey(stream, param string) string {
	// First try: stream key as query param (?key=xxx)
	if param != "" {
		param = strings.TrimPrefix(param, "?")
		for _, part := range strings.Split(param, "&") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 && kv[0] == "key" {
				return kv[1]
			}
		}
	}

	// Second try: stream name IS the stream key
	// OBS can send: rtmp://host/live/STREAM_KEY
	// SRS parses as app="live", stream="STREAM_KEY"
	if stream != "" {
		return stream
	}

	return ""
}

// transcodeVariantRegex matches stream names that end with a variant suffix like _720p, _480p, _1080p
var transcodeVariantRegex = regexp.MustCompile(`^.+_\d+p$`)

// isTranscodeVariant checks if a stream key is a transcoded variant (e.g., key_720p)
func isTranscodeVariant(streamKey string) bool {
	return transcodeVariantRegex.MatchString(streamKey)
}

func writeHookResponse(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(srsHookResponse{Code: code})
}
