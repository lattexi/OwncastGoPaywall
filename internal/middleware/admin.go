package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/laurikarhu/stream-paywall/internal/config"
)

// AdminMiddleware handles admin API authentication
type AdminMiddleware struct {
	cfg *config.Config
}

// NewAdminMiddleware creates a new admin middleware
func NewAdminMiddleware(cfg *config.Config) *AdminMiddleware {
	return &AdminMiddleware{cfg: cfg}
}

// RequireAdmin returns a middleware that requires a valid admin API key
func (m *AdminMiddleware) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("X-Admin-Key")
		if apiKey == "" {
			http.Error(w, "Missing API key", http.StatusUnauthorized)
			return
		}

		// Constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(apiKey), []byte(m.cfg.AdminAPIKey)) != 1 {
			http.Error(w, "Invalid API key", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
