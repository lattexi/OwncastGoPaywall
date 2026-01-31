package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

const (
	// AdminSessionCookieName is the name of the admin session cookie
	AdminSessionCookieName = "admin_session"
	// AdminSessionDuration is how long admin sessions last
	AdminSessionDuration = 24 * time.Hour
)

// Admin context keys
type adminContextKey string

const (
	AdminSessionContextKey adminContextKey = "admin_session"
	AdminUserContextKey    adminContextKey = "admin_user"
)

// AdminSessionMiddleware handles admin session authentication
type AdminSessionMiddleware struct {
	pgStore *storage.PostgresStore
	redis   *storage.RedisStore
}

// NewAdminSessionMiddleware creates a new admin session middleware
func NewAdminSessionMiddleware(pgStore *storage.PostgresStore, redis *storage.RedisStore) *AdminSessionMiddleware {
	return &AdminSessionMiddleware{
		pgStore: pgStore,
		redis:   redis,
	}
}

// RequireAdminSession returns a middleware that requires a valid admin session
func (m *AdminSessionMiddleware) RequireAdminSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get session cookie
		cookie, err := r.Cookie(AdminSessionCookieName)
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}

		ctx := r.Context()

		// Get session from Redis
		session, err := m.redis.GetAdminSession(ctx, cookie.Value)
		if err != nil {
			log.Error().Err(err).Msg("Failed to get admin session")
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}
		if session == nil {
			// Session expired or invalid
			m.clearSessionCookie(w)
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}

		// Check if session is expired
		if time.Now().After(session.ExpiresAt) {
			m.redis.DeleteAdminSession(ctx, session.SessionID)
			m.clearSessionCookie(w)
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}

		// Refresh session TTL
		m.redis.RefreshAdminSession(ctx, session.SessionID, AdminSessionDuration)

		// Add session to context
		ctx = context.WithValue(ctx, AdminSessionContextKey, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CreateSession creates a new admin session
func (m *AdminSessionMiddleware) CreateSession(ctx context.Context, user *storage.AdminUser) (string, error) {
	// Generate session ID
	sessionID, err := generateSessionID()
	if err != nil {
		return "", err
	}

	session := &storage.AdminSession{
		SessionID: sessionID,
		UserID:    user.ID.String(),
		Username:  user.Username,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(AdminSessionDuration),
	}

	// Store in Redis
	if err := m.redis.SetAdminSession(ctx, session, AdminSessionDuration); err != nil {
		return "", err
	}

	// Update last login
	m.pgStore.UpdateAdminLastLogin(ctx, user.ID)

	return sessionID, nil
}

// SetSessionCookie sets the admin session cookie
func (m *AdminSessionMiddleware) SetSessionCookie(w http.ResponseWriter, r *http.Request, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminSessionCookieName,
		Value:    sessionID,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(AdminSessionDuration.Seconds()),
	})
}

// ClearSession clears the admin session
func (m *AdminSessionMiddleware) ClearSession(ctx context.Context, w http.ResponseWriter, sessionID string) {
	if sessionID != "" {
		m.redis.DeleteAdminSession(ctx, sessionID)
	}
	m.clearSessionCookie(w)
}

// clearSessionCookie removes the session cookie
func (m *AdminSessionMiddleware) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminSessionCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// GetAdminSession retrieves the admin session from context
func GetAdminSession(ctx context.Context) *storage.AdminSession {
	if session, ok := ctx.Value(AdminSessionContextKey).(*storage.AdminSession); ok {
		return session
	}
	return nil
}

// generateSessionID generates a cryptographically secure session ID
func generateSessionID() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
