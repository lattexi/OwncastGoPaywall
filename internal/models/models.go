package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// StreamStatus represents the current state of a stream
type StreamStatus string

const (
	StreamStatusScheduled StreamStatus = "scheduled"
	StreamStatusLive      StreamStatus = "live"
	StreamStatusEnded     StreamStatus = "ended"
)

// TranscodeVariant represents a video transcoding quality variant
type TranscodeVariant struct {
	Name         string `json:"name"`
	VideoBitrate int    `json:"videoBitrate"`
	AudioBitrate int    `json:"audioBitrate"`
	Framerate    int    `json:"framerate"`
	ScaleHeight  int    `json:"scaleHeight,omitempty"`
}

// Stream represents a paywall-protected video stream
type Stream struct {
	ID          uuid.UUID    `json:"id"`
	Slug        string       `json:"slug"`
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	PriceCents  int          `json:"price_cents"` // Price in cents (e.g., 990 = 9.90€)
	StartTime   *time.Time   `json:"start_time,omitempty"`
	EndTime     *time.Time   `json:"end_time,omitempty"`
	Status      StreamStatus `json:"status"`
	OwncastURL  string       `json:"-"` // Never expose to clients (auto-generated)
	MaxViewers  int          `json:"max_viewers,omitempty"` // 0 = unlimited
	CreatedAt   time.Time    `json:"created_at"`

	// Streaming fields
	StreamKey       string          `json:"-"`                        // OBS stream key (never expose)
	RTMPPort        int             `json:"rtmp_port"`                // Fixed RTMP port (19350)
	ContainerName   string          `json:"-"`                        // Legacy, kept for compat
	ContainerStatus string          `json:"container_status"`         // Legacy, defaults to "stopped"
	IsPublishing    bool            `json:"is_publishing"`            // Set by SRS webhooks
	TranscodeConfig json.RawMessage `json:"transcode_config,omitempty"` // JSONB array of TranscodeVariant
}

// PriceEuros returns the price formatted in euros
func (s *Stream) PriceEuros() float64 {
	return float64(s.PriceCents) / 100
}

// PaymentStatus represents the state of a payment
type PaymentStatus string

const (
	PaymentStatusPending   PaymentStatus = "pending"
	PaymentStatusCompleted PaymentStatus = "completed"
	PaymentStatusFailed    PaymentStatus = "failed"
	PaymentStatusRefunded  PaymentStatus = "refunded"
)

// Payment represents a payment for stream access
type Payment struct {
	ID                   uuid.UUID     `json:"id"`
	StreamID             uuid.UUID     `json:"stream_id"`
	Email                string        `json:"email"`
	AmountCents          int           `json:"amount_cents"`
	Status               PaymentStatus `json:"status"`
	PaytrailRef          string        `json:"paytrail_ref,omitempty"`
	PaytrailTransactionID string       `json:"paytrail_transaction_id,omitempty"`
	AccessToken          string        `json:"-"` // Never expose directly
	TokenExpiry          *time.Time    `json:"token_expiry,omitempty"`
	CreatedAt            time.Time     `json:"created_at"`
}

// IsTokenValid checks if the access token is still valid
func (p *Payment) IsTokenValid() bool {
	if p.Status != PaymentStatusCompleted {
		return false
	}
	if p.TokenExpiry == nil {
		return false
	}
	return time.Now().Before(*p.TokenExpiry)
}

// ActiveSession represents a currently active viewing session
type ActiveSession struct {
	Token     string    `json:"token"`
	StreamID  uuid.UUID `json:"stream_id"`
	DeviceID  string    `json:"device_id"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	LastSeen  time.Time `json:"last_seen"`
}

// DeviceInfo stores information about the active device for a token
type DeviceInfo struct {
	DeviceID  string    `json:"device_id"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	LastSeen  time.Time `json:"last_seen"`
}

// CreateStreamRequest is the request body for creating a stream
type CreateStreamRequest struct {
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	PriceCents  int        `json:"price_cents"`
	StartTime   *time.Time `json:"start_time,omitempty"`
	EndTime     *time.Time `json:"end_time,omitempty"`
	MaxViewers  int        `json:"max_viewers,omitempty"`
	// Note: OwncastURL, StreamKey, RTMPPort, ContainerName are auto-generated
}

// UpdateStreamRequest is the request body for updating a stream
type UpdateStreamRequest struct {
	Title       *string       `json:"title,omitempty"`
	Description *string       `json:"description,omitempty"`
	PriceCents  *int          `json:"price_cents,omitempty"`
	StartTime   *time.Time    `json:"start_time,omitempty"`
	EndTime     *time.Time    `json:"end_time,omitempty"`
	Status      *StreamStatus `json:"status,omitempty"`
	MaxViewers  *int          `json:"max_viewers,omitempty"`
}

// CreatePaymentRequest is the request body for initiating a payment
type CreatePaymentRequest struct {
	StreamSlug string `json:"stream_slug"`
	Email      string `json:"email"`
}

// RecoverTokenRequest is the request body for token recovery
type RecoverTokenRequest struct {
	StreamSlug string `json:"stream_slug"`
	Email      string `json:"email"`
}

// PaymentCallbackParams are the query parameters from Paytrail callback
type PaymentCallbackParams struct {
	Account       string `json:"checkout-account"`
	Algorithm     string `json:"checkout-algorithm"`
	Amount        int    `json:"checkout-amount"`
	Stamp         string `json:"checkout-stamp"`
	Reference     string `json:"checkout-reference"`
	TransactionID string `json:"checkout-transaction-id"`
	Status        string `json:"checkout-status"`
	Provider      string `json:"checkout-provider"`
	Signature     string `json:"signature"`
}

// APIError represents an error response
type APIError struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// APISuccess represents a success response
type APISuccess struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// WhitelistEntry represents an email whitelisted for free stream access
type WhitelistEntry struct {
	ID        uuid.UUID `json:"id"`
	StreamID  uuid.UUID `json:"stream_id"`
	Email     string    `json:"email"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
