package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/config"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/paytrail"
	"github.com/laurikarhu/stream-paywall/internal/storage"
	"github.com/rs/zerolog/log"
)

// PaymentHandler handles payment-related endpoints
type PaymentHandler struct {
	cfg       *config.Config
	pgStore   *storage.PostgresStore
	redis     *storage.RedisStore
	paytrail  *paytrail.Client
}

// NewPaymentHandler creates a new payment handler
func NewPaymentHandler(cfg *config.Config, pgStore *storage.PostgresStore, redis *storage.RedisStore) *PaymentHandler {
	return &PaymentHandler{
		cfg:      cfg,
		pgStore:  pgStore,
		redis:    redis,
		paytrail: paytrail.NewClient(cfg.PaytrailMerchantID, cfg.PaytrailSecretKey),
	}
}

// CreatePayment initiates a new payment
// POST /api/payment/create
func (h *PaymentHandler) CreatePayment(w http.ResponseWriter, r *http.Request) {
	var req models.CreatePaymentRequest
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

	// Get stream
	stream, err := h.pgStore.GetStreamBySlug(ctx, req.StreamSlug)
	if err != nil {
		log.Error().Err(err).Str("slug", req.StreamSlug).Msg("Failed to get stream")
		writeJSONError(w, http.StatusInternalServerError, "Failed to get stream")
		return
	}
	if stream == nil {
		writeJSONError(w, http.StatusNotFound, "Stream not found")
		return
	}

	// Check if stream is available for purchase
	if stream.Status == models.StreamStatusEnded {
		writeJSONError(w, http.StatusBadRequest, "Stream has ended")
		return
	}

	// Generate unique stamp for this payment
	paymentID := uuid.New()
	stamp := paymentID.String()

	// Create payment record in database
	payment := &models.Payment{
		ID:          paymentID,
		StreamID:    stream.ID,
		Email:       req.Email,
		AmountCents: stream.PriceCents,
		Status:      models.PaymentStatusPending,
		PaytrailRef: stamp,
		CreatedAt:   time.Now(),
	}

	if err := h.pgStore.CreatePayment(ctx, payment); err != nil {
		log.Error().Err(err).Msg("Failed to create payment record")
		writeJSONError(w, http.StatusInternalServerError, "Failed to create payment")
		return
	}

	// Create Paytrail payment
	successURL := h.cfg.BaseURL + "/api/callback/success"
	cancelURL := h.cfg.BaseURL + "/api/callback/cancel"
	callbackURL := h.cfg.BaseURL + "/api/callback/success" // Server-to-server

	paytrailReq := &paytrail.SimplePaymentRequest{
		Stamp:       stamp,
		Reference:   stream.Slug + "-" + stamp[:8],
		Amount:      stream.PriceCents,
		Description: "Access to: " + stream.Title,
		Email:       req.Email,
		SuccessURL:  successURL,
		CancelURL:   cancelURL,
		CallbackURL: callbackURL,
		Language:    "FI",
	}

	paytrailResp, err := h.paytrail.CreateSimplePayment(ctx, paytrailReq)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create Paytrail payment")
		writeJSONError(w, http.StatusInternalServerError, "Failed to initiate payment")
		return
	}

	log.Info().
		Str("payment_id", paymentID.String()).
		Str("transaction_id", paytrailResp.TransactionID).
		Str("stream", stream.Slug).
		Str("email", req.Email).
		Msg("Payment initiated")

	// Return the payment redirect URL
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"redirect_url":   paytrailResp.Href,
		"transaction_id": paytrailResp.TransactionID,
		"payment_id":     paymentID.String(),
	})
}

// HandleSuccessCallback handles successful payment callbacks
// GET /api/callback/success
func (h *PaymentHandler) HandleSuccessCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Verify signature
	if !paytrail.VerifyCallbackSignature(h.cfg.PaytrailSecretKey, r.URL.Query()) {
		log.Warn().Str("query", r.URL.RawQuery).Msg("Invalid callback signature")
		writeJSONError(w, http.StatusForbidden, "Invalid signature")
		return
	}

	// Extract callback params
	params := paytrail.ExtractCallbackParams(r.URL.Query())

	log.Info().
		Str("stamp", params.Stamp).
		Str("status", params.Status).
		Str("transaction_id", params.TransactionID).
		Msg("Payment callback received")

	// Get payment by stamp
	payment, err := h.pgStore.GetPaymentByPaytrailRef(ctx, params.Stamp)
	if err != nil {
		log.Error().Err(err).Str("stamp", params.Stamp).Msg("Failed to get payment")
		writeJSONError(w, http.StatusInternalServerError, "Failed to process callback")
		return
	}
	if payment == nil {
		log.Warn().Str("stamp", params.Stamp).Msg("Payment not found")
		writeJSONError(w, http.StatusNotFound, "Payment not found")
		return
	}

	// Check if already processed
	if payment.Status == models.PaymentStatusCompleted {
		log.Info().Str("payment_id", payment.ID.String()).Msg("Payment already completed")
		// Redirect to watch page
		stream, _ := h.pgStore.GetStreamByID(ctx, payment.StreamID)
		if stream != nil {
			h.redirectToWatch(w, r, stream.Slug, payment.AccessToken)
			return
		}
		http.Redirect(w, r, h.cfg.BaseURL, http.StatusFound)
		return
	}

	// Process based on status
	if params.IsSuccessful() {
		// Generate access token
		accessToken, err := generateAccessToken()
		if err != nil {
			log.Error().Err(err).Msg("Failed to generate access token")
			writeJSONError(w, http.StatusInternalServerError, "Failed to process payment")
			return
		}

		// Set token expiry
		tokenExpiry := time.Now().Add(h.cfg.SessionDuration)

		// Update payment status
		err = h.pgStore.UpdatePaymentStatus(
			ctx,
			payment.ID,
			models.PaymentStatusCompleted,
			params.TransactionID,
			accessToken,
			&tokenExpiry,
		)
		if err != nil {
			log.Error().Err(err).Msg("Failed to update payment status")
			writeJSONError(w, http.StatusInternalServerError, "Failed to process payment")
			return
		}

		// Create session in Redis
		session := &storage.SessionData{
			Token:     accessToken,
			StreamID:  payment.StreamID.String(),
			Email:     payment.Email,
			PaymentID: payment.ID.String(),
			ExpiresAt: tokenExpiry,
		}
		if err := h.redis.SetSession(ctx, accessToken, session, h.cfg.SessionDuration); err != nil {
			log.Error().Err(err).Msg("Failed to create session")
			// Continue anyway - the database has the token
		}

		log.Info().
			Str("payment_id", payment.ID.String()).
			Str("stream_id", payment.StreamID.String()).
			Msg("Payment completed successfully")

		// Get stream and redirect to watch page
		stream, _ := h.pgStore.GetStreamByID(ctx, payment.StreamID)
		if stream != nil {
			h.redirectToWatch(w, r, stream.Slug, accessToken)
			return
		}
	} else if params.IsPending() {
		log.Info().Str("payment_id", payment.ID.String()).Msg("Payment pending")
		// Show pending page
		http.Redirect(w, r, h.cfg.BaseURL+"/payment/pending?ref="+params.Stamp, http.StatusFound)
		return
	} else {
		// Payment failed
		err = h.pgStore.UpdatePaymentStatus(ctx, payment.ID, models.PaymentStatusFailed, params.TransactionID, "", nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to update payment status")
		}
		log.Info().Str("payment_id", payment.ID.String()).Msg("Payment failed")
	}

	// Default redirect
	http.Redirect(w, r, h.cfg.BaseURL, http.StatusFound)
}

// HandleCancelCallback handles cancelled payment callbacks
// GET /api/callback/cancel
func (h *PaymentHandler) HandleCancelCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Always verify signature - Paytrail signs all callbacks
	if !paytrail.VerifyCallbackSignature(h.cfg.PaytrailSecretKey, r.URL.Query()) {
		log.Warn().Str("query", r.URL.RawQuery).Msg("Invalid or missing cancel callback signature")
		writeJSONError(w, http.StatusForbidden, "Invalid signature")
		return
	}

	params := paytrail.ExtractCallbackParams(r.URL.Query())

	log.Info().
		Str("stamp", params.Stamp).
		Str("status", params.Status).
		Msg("Payment cancelled")

	// Update payment status if stamp is provided
	if params.Stamp != "" {
		payment, err := h.pgStore.GetPaymentByPaytrailRef(ctx, params.Stamp)
		if err == nil && payment != nil && payment.Status == models.PaymentStatusPending {
			h.pgStore.UpdatePaymentStatus(ctx, payment.ID, models.PaymentStatusFailed, "", "", nil)
		}
	}

	// Redirect back to stream page or home
	// TODO: Get stream slug from stamp and redirect there
	http.Redirect(w, r, h.cfg.BaseURL, http.StatusFound)
}

// redirectToWatch sets the access token cookie and redirects to watch page
func (h *PaymentHandler) redirectToWatch(w http.ResponseWriter, r *http.Request, slug, token string) {
	// Set access token cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "access_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(h.cfg.SessionDuration.Seconds()),
	})

	http.Redirect(w, r, h.cfg.BaseURL+"/watch/"+slug, http.StatusFound)
}

// generateAccessToken generates a secure random access token
func generateAccessToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// Helper functions for JSON responses

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, models.APIError{Error: message})
}
