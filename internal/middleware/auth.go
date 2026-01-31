package middleware

import (
	"context"
	"net/http"

	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// Context keys for authenticated data
type contextKey string

const (
	TokenContextKey   contextKey = "token"
	SessionContextKey contextKey = "session"
	PaymentContextKey contextKey = "payment"
)

// AuthMiddleware handles authentication for protected routes
type AuthMiddleware struct {
	cfg     *config.Config
	pgStore *storage.PostgresStore
	redis   *storage.RedisStore
}

// NewAuthMiddleware creates a new auth middleware
func NewAuthMiddleware(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore) *AuthMiddleware {
	return &AuthMiddleware{
		cfg:     cfg,
		pgStore: pgStore,
		redis:   redis,
	}
}

// RequireAuth returns a middleware that requires a valid access token
func (m *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := m.extractToken(r)
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		ctx := r.Context()

		// Try Redis session first (faster)
		session, err := m.redis.GetSession(ctx, token)
		if err != nil {
			log.Error().Err(err).Msg("Failed to get session from Redis")
		}

		if session != nil {
			// Valid session found in Redis
			ctx = context.WithValue(ctx, TokenContextKey, token)
			ctx = context.WithValue(ctx, SessionContextKey, session)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Fall back to database
		payment, err := m.pgStore.GetPaymentByAccessToken(ctx, token)
		if err != nil {
			log.Error().Err(err).Msg("Failed to get payment by token")
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}

		if payment == nil || !payment.IsTokenValid() {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Recreate session in Redis for future requests
		session = &storage.SessionData{
			Token:     token,
			StreamID:  payment.StreamID.String(),
			Email:     payment.Email,
			PaymentID: payment.ID.String(),
			ExpiresAt: *payment.TokenExpiry,
		}
		if err := m.redis.SetSession(ctx, token, session, m.cfg.SessionDuration); err != nil {
			log.Warn().Err(err).Msg("Failed to cache session in Redis")
		}

		ctx = context.WithValue(ctx, TokenContextKey, token)
		ctx = context.WithValue(ctx, SessionContextKey, session)
		ctx = context.WithValue(ctx, PaymentContextKey, payment)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OptionalAuth returns a middleware that checks for auth but doesn't require it
func (m *AuthMiddleware) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := m.extractToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()

		// Try to get session
		session, _ := m.redis.GetSession(ctx, token)
		if session != nil {
			ctx = context.WithValue(ctx, TokenContextKey, token)
			ctx = context.WithValue(ctx, SessionContextKey, session)
		} else {
			// Try database
			payment, _ := m.pgStore.GetPaymentByAccessToken(ctx, token)
			if payment != nil && payment.IsTokenValid() {
				ctx = context.WithValue(ctx, TokenContextKey, token)
				ctx = context.WithValue(ctx, PaymentContextKey, payment)
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// extractToken gets the access token from cookie, header, or query param
func (m *AuthMiddleware) extractToken(r *http.Request) string {
	// Try cookie first
	if cookie, err := r.Cookie("access_token"); err == nil && cookie.Value != "" {
		return cookie.Value
	}

	// Try Authorization header
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}

	// Try query parameter (for HLS requests)
	if token := r.URL.Query().Get("token"); token != "" {
		return token
	}

	return ""
}

// GetToken retrieves the token from context
func GetToken(ctx context.Context) string {
	if token, ok := ctx.Value(TokenContextKey).(string); ok {
		return token
	}
	return ""
}

// GetSession retrieves the session from context
func GetSession(ctx context.Context) *storage.SessionData {
	if session, ok := ctx.Value(SessionContextKey).(*storage.SessionData); ok {
		return session
	}
	return nil
}
