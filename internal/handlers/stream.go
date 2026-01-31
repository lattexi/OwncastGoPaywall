package handlers

import (
	"bufio"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/security"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// StreamHandler handles stream-related endpoints
type StreamHandler struct {
	cfg       *config.Config
	pgStore   *storage.PostgresStore
	redis     *storage.RedisStore
	urlSigner *security.URLSigner
	client    *http.Client
}

// NewStreamHandler creates a new stream handler
func NewStreamHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore) *StreamHandler {
	return &StreamHandler{
		cfg:       cfg,
		pgStore:   pgStore,
		redis:     redis,
		urlSigner: security.NewURLSigner(cfg.SigningSecret, cfg.SignatureValidity),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ServeHLS handles HLS playlist and segment requests
// GET /stream/{streamID}/hls/{path...}
func (h *StreamHandler) ServeHLS(w http.ResponseWriter, r *http.Request) {
	// Extract stream ID and HLS path from URL
	// URL format: /stream/{streamID}/hls/{hlsPath}
	path := r.URL.Path
	
	// Parse: /stream/{streamID}/hls/{...}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	
	streamID := parts[1]
	hlsPath := strings.Join(parts[3:], "/") // Everything after /hls/
	
	ctx := r.Context()
	
	// Parse stream UUID
	streamUUID, err := uuid.Parse(streamID)
	if err != nil {
		http.Error(w, "Invalid stream ID", http.StatusBadRequest)
		return
	}
	
	// Get stream from database
	stream, err := h.pgStore.GetStreamByID(ctx, streamUUID)
	if err != nil {
		log.Error().Err(err).Str("stream_id", streamID).Msg("Failed to get stream")
		http.Error(w, "Stream not found", http.StatusNotFound)
		return
	}
	if stream == nil {
		http.Error(w, "Stream not found", http.StatusNotFound)
		return
	}
	
	// Verify the signed URL
	err = h.urlSigner.VerifyURLFromRequest(streamID, "/stream/"+streamID+"/hls/"+hlsPath, r.URL.Query())
	if err != nil {
		log.Warn().
			Err(err).
			Str("stream_id", streamID).
			Str("path", hlsPath).
			Msg("Invalid signature")
		http.Error(w, "Invalid or expired signature", http.StatusForbidden)
		return
	}
	
	// Extract token for device validation
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Missing token", http.StatusUnauthorized)
		return
	}
	
	// Validate token exists and is valid
	payment, err := h.pgStore.GetPaymentByAccessToken(ctx, token)
	if err != nil || payment == nil || !payment.IsTokenValid() {
		http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
		return
	}
	
	// Verify token is for this stream
	if payment.StreamID != streamUUID {
		http.Error(w, "Token not valid for this stream", http.StatusForbidden)
		return
	}
	
	// Build internal Owncast URL
	owncastURL := strings.TrimSuffix(stream.OwncastURL, "/") + "/hls/" + hlsPath
	
	// Determine content type based on file extension
	isPlaylist := strings.HasSuffix(hlsPath, ".m3u8")
	
	if isPlaylist {
		h.servePlaylist(w, r, stream, owncastURL, token, hlsPath)
	} else {
		h.serveSegment(w, r, owncastURL)
	}
}

// servePlaylist fetches and rewrites an HLS playlist
func (h *StreamHandler) servePlaylist(w http.ResponseWriter, r *http.Request, stream *models.Stream, owncastURL, token string, hlsPath string) {
	// Fetch original playlist from Owncast
	resp, err := h.client.Get(owncastURL)
	if err != nil {
		log.Error().Err(err).Str("url", owncastURL).Msg("Failed to fetch playlist")
		http.Error(w, "Failed to fetch stream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Str("url", owncastURL).Msg("Owncast returned non-200")
		http.Error(w, "Stream not available", resp.StatusCode)
		return
	}
	
	// Determine the base directory for this playlist (for relative URL resolution)
	baseDir := ""
	if idx := strings.LastIndex(hlsPath, "/"); idx > 0 {
		baseDir = hlsPath[:idx+1] // Include trailing slash
	}
	
	// Read and rewrite playlist
	rewritten, err := h.rewritePlaylist(resp.Body, stream.ID.String(), token, baseDir)
	if err != nil {
		log.Error().Err(err).Msg("Failed to rewrite playlist")
		http.Error(w, "Failed to process stream", http.StatusInternalServerError)
		return
	}
	
	// Set headers
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	
	w.Write([]byte(rewritten))
}

// rewritePlaylist rewrites all URLs in an HLS playlist to point to our proxy
// baseDir is the directory prefix for relative URLs (e.g., "0/" for variant playlists)
func (h *StreamHandler) rewritePlaylist(body io.Reader, streamID, token, baseDir string) (string, error) {
	var result strings.Builder
	scanner := bufio.NewScanner(body)
	
	// Regex to match segment/playlist URLs
	urlRegex := regexp.MustCompile(`^[^#].*\.(ts|m4s|m3u8)(\?.*)?$`)
	
	for scanner.Scan() {
		line := scanner.Text()
		
		// Check if this line is a URL (segment or nested playlist)
		if urlRegex.MatchString(line) {
			// Extract the filename/path
			originalPath := line
			
			// Remove any existing query params
			if idx := strings.Index(originalPath, "?"); idx != -1 {
				originalPath = originalPath[:idx]
			}
			
			// Handle relative paths - prepend the base directory
			if !strings.HasPrefix(originalPath, "/") && !strings.HasPrefix(originalPath, "http") {
				originalPath = baseDir + originalPath
			}
			
			// Build the proxy URL with signature
			proxyPath := "/stream/" + streamID + "/hls/" + originalPath
			signedURL := h.urlSigner.SignURL(streamID, token, proxyPath)
			
			result.WriteString(signedURL)
		} else {
			result.WriteString(line)
		}
		result.WriteString("\n")
	}
	
	if err := scanner.Err(); err != nil {
		return "", err
	}
	
	return result.String(), nil
}

// serveSegment proxies a video segment from Owncast
func (h *StreamHandler) serveSegment(w http.ResponseWriter, r *http.Request, owncastURL string) {
	// Fetch segment from Owncast
	resp, err := h.client.Get(owncastURL)
	if err != nil {
		log.Error().Err(err).Str("url", owncastURL).Msg("Failed to fetch segment")
		http.Error(w, "Failed to fetch segment", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Segment not available", resp.StatusCode)
		return
	}
	
	// Copy relevant headers
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		// Default for TS segments
		w.Header().Set("Content-Type", "video/mp2t")
	}
	
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	
	// Allow caching of segments (they're immutable)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	
	// Stream the content
	io.Copy(w, resp.Body)
}

// GetStreamInfo returns public stream information
// GET /api/streams/{slug}
func (h *StreamHandler) GetStreamInfo(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		writeJSONError(w, http.StatusBadRequest, "Stream slug is required")
		return
	}
	
	ctx := r.Context()
	stream, err := h.pgStore.GetStreamBySlug(ctx, slug)
	if err != nil {
		log.Error().Err(err).Str("slug", slug).Msg("Failed to get stream")
		writeJSONError(w, http.StatusInternalServerError, "Failed to get stream")
		return
	}
	if stream == nil {
		writeJSONError(w, http.StatusNotFound, "Stream not found")
		return
	}
	
	// Return public info (OwncastURL is omitted via json:"-" tag)
	writeJSON(w, http.StatusOK, stream)
}

// ListStreams returns all available streams
// GET /api/streams
func (h *StreamHandler) ListStreams(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	streams, err := h.pgStore.ListActiveStreams(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list streams")
		writeJSONError(w, http.StatusInternalServerError, "Failed to list streams")
		return
	}
	
	// OwncastURL is already hidden by json:"-" tag
	writeJSON(w, http.StatusOK, streams)
}

// Heartbeat updates the session last seen time
// POST /api/stream/{id}/heartbeat
func (h *StreamHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	streamID := r.PathValue("id")
	
	// Get token from cookie or header
	token := ""
	if cookie, err := r.Cookie("access_token"); err == nil {
		token = cookie.Value
	}
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized, "Missing access token")
		return
	}
	
	ctx := r.Context()
	
	// Validate token
	payment, err := h.pgStore.GetPaymentByAccessToken(ctx, token)
	if err != nil || payment == nil || !payment.IsTokenValid() {
		writeJSONError(w, http.StatusUnauthorized, "Invalid or expired token")
		return
	}
	
	// Verify stream ID matches
	if streamID != payment.StreamID.String() {
		writeJSONError(w, http.StatusForbidden, "Token not valid for this stream")
		return
	}
	
	// Refresh session TTL
	h.redis.RefreshSession(ctx, token, h.cfg.SessionDuration)
	
	// Track active session for viewer count (TTL slightly longer than heartbeat interval)
	h.redis.TrackActiveSession(ctx, payment.StreamID, token, 45*time.Second)
	
	// Generate fresh signed playlist URL for the client
	playlistPath := "/stream/" + streamID + "/hls/stream.m3u8"
	signedURL := h.cfg.BaseURL + h.urlSigner.SignURL(streamID, token, playlistPath)
	
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"message":      "Heartbeat received",
		"playlist_url": signedURL,
	})
}

// GetPlaylistURL returns a signed playlist URL for a stream
// This is called after successful authentication to get the initial playlist URL
func (h *StreamHandler) GetPlaylistURL(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	
	// Get token from cookie
	token := ""
	if cookie, err := r.Cookie("access_token"); err == nil {
		token = cookie.Value
	}
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized, "Missing access token")
		return
	}
	
	ctx := r.Context()
	
	// Get stream
	stream, err := h.pgStore.GetStreamBySlug(ctx, slug)
	if err != nil || stream == nil {
		writeJSONError(w, http.StatusNotFound, "Stream not found")
		return
	}
	
	// Validate token
	payment, err := h.pgStore.GetPaymentByAccessToken(ctx, token)
	if err != nil || payment == nil || !payment.IsTokenValid() {
		writeJSONError(w, http.StatusUnauthorized, "Invalid or expired token")
		return
	}
	
	// Verify token is for this stream
	if payment.StreamID != stream.ID {
		writeJSONError(w, http.StatusForbidden, "Token not valid for this stream")
		return
	}
	
	// Generate signed playlist URL
	playlistPath := "/stream/" + stream.ID.String() + "/hls/stream.m3u8"
	signedURL := h.cfg.BaseURL + h.urlSigner.SignURL(stream.ID.String(), token, playlistPath)
	
	writeJSON(w, http.StatusOK, map[string]string{
		"playlist_url": signedURL,
	})
}

// BuildSignedPlaylistURL is a helper that builds a signed playlist URL
// Used by page handlers to embed the URL in templates
func (h *StreamHandler) BuildSignedPlaylistURL(streamID uuid.UUID, token string) string {
	playlistPath := "/stream/" + streamID.String() + "/hls/stream.m3u8"
	return h.cfg.BaseURL + h.urlSigner.SignURL(streamID.String(), token, playlistPath)
}
