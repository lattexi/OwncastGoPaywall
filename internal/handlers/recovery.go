package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// RecoveryHandler handles token recovery endpoints
type RecoveryHandler struct {
	cfg     *config.Config
	pgStore *storage.PostgresStore
	redis   *storage.RedisStore
}

// NewRecoveryHandler creates a new recovery handler
func NewRecoveryHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore) *RecoveryHandler {
	return &RecoveryHandler{
		cfg:     cfg,
		pgStore: pgStore,
		redis:   redis,
	}
}

// RecoverToken handles token recovery requests
// POST /api/payment/recover
func (h *RecoveryHandler) RecoverToken(w http.ResponseWriter, r *http.Request) {
	var req models.RecoverTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate request
	if req.StreamSlug == "" {
		writeJSONError(w, http.StatusBadRequest, "stream_slug is required")
		return
	}
	if req.Email == "" {
		writeJSONError(w, http.StatusBadRequest, "email is required")
		return
	}

	ctx := r.Context()

	// Get client IP for rate limiting
	clientIP := getClientIP(r)

	// Check rate limits
	allowed, err := h.redis.CheckRecoveryRateLimit(
		ctx,
		req.Email,
		clientIP,
		h.cfg.RecoveryRateLimitPerEmail,
		h.cfg.RecoveryRateLimitPerIP,
	)
	if err != nil {
		log.Error().Err(err).Msg("Failed to check rate limit")
		// Continue anyway to not leak information
	}
	if !allowed {
		log.Warn().
			Str("email", req.Email).
			Str("ip", clientIP).
			Msg("Recovery rate limit exceeded")
		writeJSONError(w, http.StatusTooManyRequests, "Too many recovery attempts. Please try again later.")
		return
	}

	// Add artificial delay to prevent timing attacks (constant time response)
	// This makes it harder to determine if an email exists
	startTime := time.Now()
	defer func() {
		elapsed := time.Since(startTime)
		if elapsed < 500*time.Millisecond {
			time.Sleep(500*time.Millisecond - elapsed)
		}
	}()

	// Get stream
	stream, err := h.pgStore.GetStreamBySlug(ctx, req.StreamSlug)
	if err != nil {
		log.Error().Err(err).Str("slug", req.StreamSlug).Msg("Failed to get stream")
		writeJSONError(w, http.StatusNotFound, "No active purchase found for this email.")
		return
	}
	if stream == nil {
		writeJSONError(w, http.StatusNotFound, "No active purchase found for this email.")
		return
	}

	// Look up completed payment for this email and stream
	payment, err := h.pgStore.GetCompletedPaymentByEmailAndStream(ctx, req.Email, stream.ID)
	if err != nil {
		log.Error().Err(err).Msg("Failed to look up payment")
		writeJSONError(w, http.StatusNotFound, "No active purchase found for this email.")
		return
	}

	// If no payment found, check if email is whitelisted
	if payment == nil {
		whitelisted, err := h.pgStore.IsEmailWhitelisted(ctx, stream.ID, req.Email)
		if err != nil {
			log.Error().Err(err).Msg("Failed to check whitelist")
		}

		if whitelisted {
			// Create a "whitelisted" payment record for this email
			payment, err = h.createWhitelistedAccess(ctx, stream, req.Email)
			if err != nil {
				log.Error().Err(err).Msg("Failed to create whitelisted access")
				writeJSONError(w, http.StatusInternalServerError, "Failed to grant access.")
				return
			}
			log.Info().
				Str("email", req.Email).
				Str("stream", req.StreamSlug).
				Msg("Whitelisted access granted")
		}
	}

	if payment == nil {
		log.Info().
			Str("email", req.Email).
			Str("stream", req.StreamSlug).
			Msg("No payment or whitelist entry found for recovery")
		writeJSONError(w, http.StatusNotFound, "No active purchase found for this email.")
		return
	}

	// Check if token is expired
	if payment.TokenExpiry != nil && time.Now().After(*payment.TokenExpiry) {
		writeJSONError(w, http.StatusGone, "Your access has expired.")
		return
	}

	// Generate new access token (invalidates old one)
	newToken, err := generateAccessToken()
	if err != nil {
		log.Error().Err(err).Msg("Failed to generate new access token")
		writeJSONError(w, http.StatusInternalServerError, "Failed to recover access.")
		return
	}

	// Set new token expiry (extend from now)
	newExpiry := time.Now().Add(h.cfg.SessionDuration)

	// Update payment with new token
	if err := h.pgStore.UpdatePaymentAccessToken(ctx, payment.ID, newToken, &newExpiry); err != nil {
		log.Error().Err(err).Msg("Failed to update access token")
		writeJSONError(w, http.StatusInternalServerError, "Failed to recover access.")
		return
	}

	// Delete old session from Redis (if exists)
	if payment.AccessToken != "" {
		h.redis.DeleteSession(ctx, payment.AccessToken)
		h.redis.DeleteActiveDevice(ctx, payment.AccessToken)
	}

	// Create new session in Redis
	session := &storage.SessionData{
		Token:     newToken,
		StreamID:  stream.ID.String(),
		Email:     payment.Email,
		PaymentID: payment.ID.String(),
		ExpiresAt: newExpiry,
	}
	if err := h.redis.SetSession(ctx, newToken, session, h.cfg.SessionDuration); err != nil {
		log.Error().Err(err).Msg("Failed to create session")
		// Continue anyway - database has the token
	}

	log.Info().
		Str("payment_id", payment.ID.String()).
		Str("email", req.Email).
		Str("stream", req.StreamSlug).
		Msg("Token recovered successfully")

	// Return success with token
	// Note: We set the cookie in the response for convenience
	http.SetCookie(w, &http.Cookie{
		Name:     "access_token",
		Value:    newToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(h.cfg.SessionDuration.Seconds()),
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"message":      "Access recovered successfully",
		"redirect_url": h.cfg.BaseURL + "/watch/" + stream.Slug,
	})
}

// getClientIP extracts the client IP address from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (for proxies)
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		// Take the first IP in the chain
		for i, c := range forwarded {
			if c == ',' {
				return forwarded[:i]
			}
		}
		return forwarded
	}

	// Check X-Real-IP header
	realIP := r.Header.Get("X-Real-IP")
	if realIP != "" {
		return realIP
	}

	// Fall back to RemoteAddr
	// Remove port if present
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

// createWhitelistedAccess creates a payment record for a whitelisted email
// This allows whitelisted users to access streams without payment
func (h *RecoveryHandler) createWhitelistedAccess(ctx context.Context, stream *models.Stream, email string) (*models.Payment, error) {
	// Generate access token
	token, err := generateAccessToken()
	if err != nil {
		return nil, err
	}

	expiry := time.Now().Add(h.cfg.SessionDuration)

	payment := &models.Payment{
		ID:          uuid.New(),
		StreamID:    stream.ID,
		Email:       email,
		AmountCents: 0, // Free access
		Status:      models.PaymentStatusCompleted,
		PaytrailRef: "whitelist", // Indicates this is a whitelisted access
		AccessToken: token,
		TokenExpiry: &expiry,
		CreatedAt:   time.Now(),
	}

	if err := h.pgStore.CreatePayment(ctx, payment); err != nil {
		return nil, err
	}

	// Create session in Redis
	session := &storage.SessionData{
		Token:     token,
		StreamID:  stream.ID.String(),
		Email:     email,
		PaymentID: payment.ID.String(),
		ExpiresAt: expiry,
	}
	if err := h.redis.SetSession(ctx, token, session, h.cfg.SessionDuration); err != nil {
		log.Error().Err(err).Msg("Failed to create session for whitelisted user")
		// Continue anyway - database has the token
	}

	return payment, nil
}
