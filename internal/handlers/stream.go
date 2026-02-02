package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/security"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
)

// hlsURLRegex matches HLS segment and playlist URLs (compiled once at package level)
var hlsURLRegex = regexp.MustCompile(`^[^#].*\.(ts|m4s|m3u8)(\?.*)?$`)

// streamCacheEntry holds cached stream data with expiry
type streamCacheEntry struct {
	stream    *models.Stream
	expiresAt time.Time
}

// playlistCacheEntry holds cached HLS playlist data with short TTL
type playlistCacheEntry struct {
	content   string
	expiresAt time.Time
}

// segmentCacheEntry holds cached HLS segment data
type segmentCacheEntry struct {
	data        []byte
	contentType string
	expiresAt   time.Time
}

// StreamHandler handles stream-related endpoints
type StreamHandler struct {
	cfg            *config.Config
	pgStore        *storage.PostgresStore
	redis          *storage.RedisStore
	urlSigner      *security.URLSigner
	sessionManager *security.SessionManager
	client         *http.Client
	streamCache    sync.Map            // uuid.UUID -> *streamCacheEntry
	playlistCache  sync.Map            // string (owncastURL) -> *playlistCacheEntry
	segmentCache   sync.Map            // string (owncastURL) -> *segmentCacheEntry
	playlistFlight singleflight.Group  // deduplicates concurrent playlist fetches
	segmentFlight  singleflight.Group  // deduplicates concurrent segment fetches
}

// NewStreamHandler creates a new stream handler
func NewStreamHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore) *StreamHandler {
	return &StreamHandler{
		cfg:            cfg,
		pgStore:        pgStore,
		redis:          redis,
		urlSigner:      security.NewURLSigner(cfg.SigningSecret, cfg.SignatureValidity),
		sessionManager: security.NewSessionManager(redis, cfg.SessionDuration, cfg.HeartbeatTimeout),
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        1000,            // Increased for high viewer counts
				MaxIdleConnsPerHost: 100,             // Per Owncast container
				MaxConnsPerHost:     0,               // No limit
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,            // Video segments are already compressed
			},
			Timeout: 30 * time.Second,
		},
	}
}

// getStreamCached returns a stream from cache or fetches from DB
func (h *StreamHandler) getStreamCached(ctx context.Context, id uuid.UUID) (*models.Stream, error) {
	// Check cache
	if entry, ok := h.streamCache.Load(id); ok {
		e := entry.(*streamCacheEntry)
		if time.Now().Before(e.expiresAt) {
			return e.stream, nil
		}
		h.streamCache.Delete(id) // Expired
	}

	// Fetch from DB
	stream, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil || stream == nil {
		return stream, err
	}

	// Cache for 60 seconds
	h.streamCache.Store(id, &streamCacheEntry{
		stream:    stream,
		expiresAt: time.Now().Add(60 * time.Second),
	})
	return stream, nil
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
	
	// Get stream from cache (or DB on cache miss)
	stream, err := h.getStreamCached(ctx, streamUUID)
	if err != nil {
		log.Error().Err(err).Str("stream_id", streamID).Msg("Failed to get stream")
		http.Error(w, "Stream not found", http.StatusNotFound)
		return
	}
	if stream == nil {
		http.Error(w, "Stream not found", http.StatusNotFound)
		return
	}

	// Check if stream is live
	if stream.Status != models.StreamStatusLive {
		http.Error(w, "Stream is not live", http.StatusForbidden)
		return
	}

	// Verify the signed URL
	// The signature validates: streamID + token + path + expiry
	// If signature is valid, the token was valid when the URL was signed
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

	// Extract token for playlist URL signing (already validated by signature above)
	token := r.URL.Query().Get("token")

	// Determine content type based on file extension
	isPlaylist := strings.HasSuffix(hlsPath, ".m3u8")

	// For playlist requests, validate session (server-side protection)
	// This prevents cheaters from bypassing client-side JavaScript checks
	// Uses Redis for fast validation instead of PostgreSQL
	// Note: Device validation is handled by the heartbeat, not here - the first playlist
	// request happens BEFORE the first heartbeat, so we can't require a recent heartbeat.
	if isPlaylist {
		// Check session in Redis (fast) - session is created on payment confirmation
		session, err := h.redis.GetSession(ctx, token)
		if err != nil || session == nil {
			log.Warn().
				Str("stream_id", streamID).
				Str("path", hlsPath).
				Msg("No session found on playlist request")
			http.Error(w, "Session expired", http.StatusUnauthorized)
			return
		}

		// Verify token is for this stream
		if session.StreamID != streamID {
			http.Error(w, "Token not valid for this stream", http.StatusForbidden)
			return
		}
	}

	// Build internal Owncast URL
	owncastURL := strings.TrimSuffix(stream.OwncastURL, "/") + "/hls/" + hlsPath

	if isPlaylist {
		h.servePlaylist(w, r, stream, owncastURL, token, hlsPath)
	} else {
		h.serveSegment(w, r, owncastURL)
	}
}

// servePlaylist fetches and rewrites an HLS playlist
func (h *StreamHandler) servePlaylist(w http.ResponseWriter, r *http.Request, stream *models.Stream, owncastURL, token string, hlsPath string) {
	// Determine the base directory for this playlist (for relative URL resolution)
	baseDir := ""
	if idx := strings.LastIndex(hlsPath, "/"); idx > 0 {
		baseDir = hlsPath[:idx+1] // Include trailing slash
	}

	// Try to get playlist from cache (reduces load on Owncast for concurrent viewers)
	// Cache key is just the owncastURL since base playlist content is the same for all viewers
	var originalPlaylist string
	if entry, ok := h.playlistCache.Load(owncastURL); ok {
		e := entry.(*playlistCacheEntry)
		if time.Now().Before(e.expiresAt) {
			originalPlaylist = e.content
		} else {
			h.playlistCache.Delete(owncastURL)
		}
	}

	// If not in cache, fetch from Owncast using singleflight to deduplicate concurrent requests
	if originalPlaylist == "" {
		result, err, _ := h.playlistFlight.Do(owncastURL, func() (interface{}, error) {
			// Double-check cache (another goroutine might have populated it)
			if entry, ok := h.playlistCache.Load(owncastURL); ok {
				e := entry.(*playlistCacheEntry)
				if time.Now().Before(e.expiresAt) {
					return e.content, nil
				}
			}

			resp, err := h.client.Get(owncastURL)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("owncast returned status %d", resp.StatusCode)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, err
			}
			content := string(body)

			// Cache for 2 seconds (HLS segments are typically 2-6 seconds)
			h.playlistCache.Store(owncastURL, &playlistCacheEntry{
				content:   content,
				expiresAt: time.Now().Add(2 * time.Second),
			})

			return content, nil
		})

		if err != nil {
			log.Error().Err(err).Str("url", owncastURL).Msg("Failed to fetch playlist")
			http.Error(w, "Failed to fetch stream", http.StatusBadGateway)
			return
		}
		originalPlaylist = result.(string)
	}

	// Rewrite playlist with signed URLs for this user's token
	rewritten, err := h.rewritePlaylist(strings.NewReader(originalPlaylist), stream.ID.String(), token, baseDir)
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

	for scanner.Scan() {
		line := scanner.Text()

		// Check if this line is a URL (segment or nested playlist)
		if hlsURLRegex.MatchString(line) {
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

// serveSegment proxies a video segment from Owncast with server-side caching
func (h *StreamHandler) serveSegment(w http.ResponseWriter, r *http.Request, owncastURL string) {
	// Try to get segment from cache (reduces load on Owncast for concurrent viewers)
	if entry, ok := h.segmentCache.Load(owncastURL); ok {
		e := entry.(*segmentCacheEntry)
		if time.Now().Before(e.expiresAt) {
			// Cache hit - serve from memory
			w.Header().Set("Content-Type", e.contentType)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(e.data)))
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(e.data)
			return
		}
		// Expired, delete it
		h.segmentCache.Delete(owncastURL)
	}

	// Cache miss - use singleflight to deduplicate concurrent fetches
	// When 10,000 viewers request the same new segment simultaneously,
	// only ONE request fetches from Owncast, others wait and share the result
	result, err, _ := h.segmentFlight.Do(owncastURL, func() (interface{}, error) {
		// Double-check cache (another goroutine might have populated it)
		if entry, ok := h.segmentCache.Load(owncastURL); ok {
			e := entry.(*segmentCacheEntry)
			if time.Now().Before(e.expiresAt) {
				return e, nil
			}
		}

		// Fetch from Owncast
		resp, err := h.client.Get(owncastURL)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("owncast returned status %d", resp.StatusCode)
		}

		// Read the segment into memory
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		// Determine content type
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "video/mp2t"
		}

		entry := &segmentCacheEntry{
			data:        data,
			contentType: contentType,
			expiresAt:   time.Now().Add(30 * time.Second),
		}

		// Cache if under 5MB
		if len(data) < 5*1024*1024 {
			h.segmentCache.Store(owncastURL, entry)
		}

		return entry, nil
	})

	if err != nil {
		log.Error().Err(err).Str("url", owncastURL).Msg("Failed to fetch segment")
		http.Error(w, "Failed to fetch segment", http.StatusBadGateway)
		return
	}

	// Serve the segment from the result
	entry := result.(*segmentCacheEntry)
	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(entry.data)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(entry.data)
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

// HeartbeatRequest represents the heartbeat request body
type HeartbeatRequest struct {
	DeviceID string `json:"device_id"`
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

	// Parse request body for device ID
	var req HeartbeatRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Device ID is optional for backwards compatibility
			req.DeviceID = ""
		}
	}

	ctx := r.Context()

	// Validate token using Redis session (fast) instead of PostgreSQL
	// Session is created when payment is confirmed, so if it exists, the token is valid
	session, err := h.redis.GetSession(ctx, token)
	if err != nil || session == nil {
		writeJSONError(w, http.StatusUnauthorized, "Invalid or expired token")
		return
	}

	// Verify stream ID matches
	if streamID != session.StreamID {
		writeJSONError(w, http.StatusForbidden, "Token not valid for this stream")
		return
	}

	// Parse stream UUID for active session tracking
	streamUUID, err := uuid.Parse(session.StreamID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Invalid stream ID in session")
		return
	}

	// Validate device if device ID is provided
	if req.DeviceID != "" {
		ip := r.RemoteAddr
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			ip = forwarded
		}
		userAgent := r.Header.Get("User-Agent")

		result, err := h.sessionManager.ValidateDevice(ctx, token, req.DeviceID, ip, userAgent)
		if err != nil {
			log.Error().Err(err).Str("token", token[:8]+"...").Msg("Device validation error")
			writeJSONError(w, http.StatusInternalServerError, "Device validation failed")
			return
		}

		if !result.Allowed {
			log.Warn().
				Str("token", token[:8]+"...").
				Str("device_id", req.DeviceID).
				Str("active_device", result.ActiveDevice).
				Dur("wait_time", result.WaitTime).
				Msg("Device rejected - another device is active")
			writeJSONError(w, http.StatusConflict, "Another device is currently watching this stream")
			return
		}
	}

	// Refresh session TTL
	h.redis.RefreshSession(ctx, token, h.cfg.SessionDuration)

	// Track active session for viewer count (TTL slightly longer than heartbeat interval)
	h.redis.TrackActiveSession(ctx, streamUUID, token, 45*time.Second)

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
