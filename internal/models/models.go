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

// ContainerStatus represents the status of a container (kept for backward compat)
type ContainerStatus string

const (
	ContainerStatusStopped  ContainerStatus = "stopped"
	ContainerStatusStarting ContainerStatus = "starting"
	ContainerStatusRunning  ContainerStatus = "running"
	ContainerStatusStopping ContainerStatus = "stopping"
	ContainerStatusError    ContainerStatus = "error"
)

// TranscodeVariant represents a single transcode quality variant for SRS/FFmpeg
type TranscodeVariant struct {
	Name        string `json:"name"`
	VBitrate    int    `json:"vbitrate"`              // Video bitrate in kbps
	VWidth      int    `json:"vwidth,omitempty"`       // Video width (e.g., 1920)
	VHeight     int    `json:"vheight,omitempty"`      // Video height (e.g., 1080)
	VFps        int    `json:"vfps,omitempty"`         // Video FPS
	VPreset     string `json:"vpreset,omitempty"`      // FFmpeg preset (ultrafast, faster, medium, slow, veryslow)
	ABitrate    int    `json:"abitrate,omitempty"`     // Audio bitrate in kbps
	Passthrough bool   `json:"passthrough,omitempty"`  // If true, pass through without transcoding
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
	OwncastURL  string       `json:"-"` // Legacy - kept for backward compat
	MaxViewers  int          `json:"max_viewers,omitempty"` // 0 = unlimited
	CreatedAt   time.Time    `json:"created_at"`

	// Container fields (legacy - ContainerStatus defaults to "stopped")
	StreamKey       string          `json:"-"`                  // OBS stream key (never expose)
	RTMPPort        int             `json:"rtmp_port"`          // RTMP port (shared across all streams)
	ContainerName   string          `json:"-"`                  // Legacy container name
	ContainerStatus ContainerStatus `json:"container_status"`   // Legacy - defaults to "stopped"

	// SRS fields
	IsPublishing    bool            `json:"is_publishing"`      // Whether OBS is currently publishing
	TranscodeConfig json.RawMessage `json:"-"`                  // JSONB transcode config
}

// PriceEuros returns the price formatted in euros
func (s *Stream) PriceEuros() float64 {
	return float64(s.PriceCents) / 100
}

// GetTranscodeVariants parses the transcode config into variant structs
func (s *Stream) GetTranscodeVariants() ([]TranscodeVariant, error) {
	if len(s.TranscodeConfig) == 0 || string(s.TranscodeConfig) == "[]" || string(s.TranscodeConfig) == "null" {
		return nil, nil
	}
	var variants []TranscodeVariant
	if err := json.Unmarshal(s.TranscodeConfig, &variants); err != nil {
		return nil, err
	}
	return variants, nil
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
}

// UpdateStreamRequest is the request body for updating a stream
type UpdateStreamRequest struct {
	Title           *string          `json:"title,omitempty"`
	Description     *string          `json:"description,omitempty"`
	PriceCents      *int             `json:"price_cents,omitempty"`
	StartTime       *time.Time       `json:"start_time,omitempty"`
	EndTime         *time.Time       `json:"end_time,omitempty"`
	Status          *StreamStatus    `json:"status,omitempty"`
	MaxViewers      *int             `json:"max_viewers,omitempty"`
	ContainerStatus *ContainerStatus `json:"container_status,omitempty"`
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
