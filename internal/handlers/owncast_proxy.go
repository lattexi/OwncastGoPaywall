package handlers

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/middleware"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// OwncastProxyHandler proxies requests to Owncast container admin panels
type OwncastProxyHandler struct {
	cfg       *config.Config
	pgStore   *storage.PostgresStore
	redis     *storage.RedisStore
	sessionMw *middleware.AdminSessionMiddleware
	client    *http.Client
}

// NewOwncastProxyHandler creates a new Owncast proxy handler
func NewOwncastProxyHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore, sessionMw *middleware.AdminSessionMiddleware) *OwncastProxyHandler {
	return &OwncastProxyHandler{
		cfg:       cfg,
		pgStore:   pgStore,
		redis:     redis,
		sessionMw: sessionMw,
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // Don't follow redirects
			},
		},
	}
}

// ProxyRequest handles proxying requests to the Owncast container
func (h *OwncastProxyHandler) ProxyRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get stream ID from path
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "Invalid stream ID", http.StatusBadRequest)
		return
	}

	// Get the path to proxy
	proxyPath := r.PathValue("path")
	if proxyPath == "" {
		proxyPath = "/"
	}
	if !strings.HasPrefix(proxyPath, "/") {
		proxyPath = "/" + proxyPath
	}

	// Get stream from database
	stream, err := h.pgStore.GetStreamByID(ctx, id)
	if err != nil || stream == nil {
		http.Error(w, "Stream not found", http.StatusNotFound)
		return
	}

	// Check that container is running
	if stream.ContainerStatus != models.ContainerStatusRunning {
		http.Error(w, "Container is not running", http.StatusServiceUnavailable)
		return
	}

	// Build target URL (internal Docker network URL)
	targetURL := fmt.Sprintf("%s%s", stream.OwncastURL, proxyPath)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Create proxy request
	proxyReq, err := http.NewRequestWithContext(ctx, r.Method, targetURL, r.Body)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create proxy request")
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	// Copy headers from original request
	for key, values := range r.Header {
		// Skip hop-by-hop headers
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}

	// Add Basic Auth for Owncast admin
	auth := base64.StdEncoding.EncodeToString([]byte("admin:" + h.cfg.OwncastAdminPassword))
	proxyReq.Header.Set("Authorization", "Basic "+auth)

	// Remove any existing auth from the client
	proxyReq.Header.Del("Cookie")

	// Set forwarded headers
	if clientIP := getClientIP(r); clientIP != "" {
		proxyReq.Header.Set("X-Forwarded-For", clientIP)
	}
	proxyReq.Header.Set("X-Forwarded-Host", r.Host)
	proxyReq.Header.Set("X-Forwarded-Proto", getProto(r))

	// Execute request
	resp, err := h.client.Do(proxyReq)
	if err != nil {
		log.Error().Err(err).Str("url", targetURL).Msg("Proxy request failed")
		http.Error(w, "Failed to reach Owncast container", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		// Skip hop-by-hop headers
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			// Rewrite Location header for redirects
			if key == "Location" {
				value = h.rewriteURL(value, stream.OwncastURL, id.String())
			}
			w.Header().Add(key, value)
		}
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error().Err(err).Msg("Failed to read proxy response")
		http.Error(w, "Failed to read response", http.StatusBadGateway)
		return
	}

	// Rewrite HTML content to fix URLs
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		body = h.rewriteHTML(body, stream.OwncastURL, id.String())
	}

	// Write status code and body
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// rewriteHTML rewrites URLs in HTML content to point to the proxy
func (h *OwncastProxyHandler) rewriteHTML(body []byte, owncastURL, streamID string) []byte {
	content := string(body)
	proxyBase := fmt.Sprintf("/admin/streams/%s/owncast", streamID)

	// Rewrite absolute URLs pointing to the Owncast container
	content = strings.ReplaceAll(content, owncastURL, proxyBase)

	// Rewrite relative URLs in href and src attributes
	// Match href="/..." and src="/..." patterns
	hrefPattern := regexp.MustCompile(`(href|src|action)="(/[^"]*)"`)
	content = hrefPattern.ReplaceAllStringFunc(content, func(match string) string {
		// Extract the attribute and path
		parts := hrefPattern.FindStringSubmatch(match)
		if len(parts) == 3 {
			attr := parts[1]
			path := parts[2]
			// Don't rewrite if it already starts with /admin/streams/
			if strings.HasPrefix(path, "/admin/streams/") {
				return match
			}
			// Don't rewrite external URLs
			if strings.HasPrefix(path, "//") {
				return match
			}
			return fmt.Sprintf(`%s="%s%s"`, attr, proxyBase, path)
		}
		return match
	})

	// Rewrite URLs in JavaScript fetch/API calls
	// Match fetch("/api/...) patterns
	fetchPattern := regexp.MustCompile(`fetch\s*\(\s*["'](/[^"']+)["']`)
	content = fetchPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := fetchPattern.FindStringSubmatch(match)
		if len(parts) == 2 {
			path := parts[1]
			if strings.HasPrefix(path, "/admin/streams/") {
				return match
			}
			return fmt.Sprintf(`fetch("%s%s"`, proxyBase, path)
		}
		return match
	})

	// Rewrite URLs in inline scripts that use string concatenation
	// Match "/api" patterns in script contexts
	apiPattern := regexp.MustCompile(`["']/(api|admin)[^"']*["']`)
	content = apiPattern.ReplaceAllStringFunc(content, func(match string) string {
		// Check if it's already rewritten
		if strings.Contains(match, "/admin/streams/") {
			return match
		}
		// Extract the quote character and path
		quote := match[0:1]
		path := match[1 : len(match)-1]
		return fmt.Sprintf(`%s%s%s%s`, quote, proxyBase, path, quote)
	})

	return []byte(content)
}

// rewriteURL rewrites a URL from Owncast internal URL to proxy URL
func (h *OwncastProxyHandler) rewriteURL(url, owncastURL, streamID string) string {
	proxyBase := fmt.Sprintf("/admin/streams/%s/owncast", streamID)

	// If URL starts with the internal Owncast URL, rewrite it
	if strings.HasPrefix(url, owncastURL) {
		return proxyBase + strings.TrimPrefix(url, owncastURL)
	}

	// If URL is a relative path starting with /
	if strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "/admin/streams/") {
		return proxyBase + url
	}

	return url
}

// isHopByHopHeader returns true if the header is a hop-by-hop header
func isHopByHopHeader(header string) bool {
	hopByHop := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailer":             true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
	}
	return hopByHop[http.CanonicalHeaderKey(header)]
}

// getProto returns the protocol (http or https) from the request
func getProto(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	return "http"
}
