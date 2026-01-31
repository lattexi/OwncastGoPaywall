package security

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/laurikarhu/stream-paywall/internal/storage"
)

// SessionManager handles session and device management
type SessionManager struct {
	redis            *storage.RedisStore
	sessionDuration  time.Duration
	heartbeatTimeout time.Duration
}

// NewSessionManager creates a new session manager
func NewSessionManager(redis *storage.RedisStore, sessionDuration, heartbeatTimeout time.Duration) *SessionManager {
	return &SessionManager{
		redis:            redis,
		sessionDuration:  sessionDuration,
		heartbeatTimeout: heartbeatTimeout,
	}
}

// CreateSession creates a new session for an access token
func (m *SessionManager) CreateSession(ctx context.Context, token string, streamID uuid.UUID, email, paymentID string) error {
	session := &storage.SessionData{
		Token:     token,
		StreamID:  streamID.String(),
		Email:     email,
		PaymentID: paymentID,
		ExpiresAt: time.Now().Add(m.sessionDuration),
	}
	return m.redis.SetSession(ctx, token, session, m.sessionDuration)
}

// GetSession retrieves a session
func (m *SessionManager) GetSession(ctx context.Context, token string) (*storage.SessionData, error) {
	return m.redis.GetSession(ctx, token)
}

// RefreshSession extends the session TTL
func (m *SessionManager) RefreshSession(ctx context.Context, token string) error {
	return m.redis.RefreshSession(ctx, token, m.sessionDuration)
}

// DeleteSession removes a session
func (m *SessionManager) DeleteSession(ctx context.Context, token string) error {
	// Delete both session and device binding
	if err := m.redis.DeleteSession(ctx, token); err != nil {
		return err
	}
	return m.redis.DeleteActiveDevice(ctx, token)
}

// DeviceValidationResult contains the result of device validation
type DeviceValidationResult struct {
	Allowed       bool
	IsNewDevice   bool
	IsSameDevice  bool
	TimedOut      bool
	ActiveDevice  string
	WaitTime      time.Duration
}

// ValidateDevice checks if a device is allowed to access the stream
func (m *SessionManager) ValidateDevice(ctx context.Context, token, deviceID, ip, userAgent string) (*DeviceValidationResult, error) {
	result := &DeviceValidationResult{}
	
	currentDevice, err := m.redis.GetActiveDevice(ctx, token)
	if err != nil {
		return nil, err
	}
	
	now := time.Now()
	
	// No device registered yet
	if currentDevice == nil {
		result.Allowed = true
		result.IsNewDevice = true
		
		device := &models.DeviceInfo{
			DeviceID:  deviceID,
			IP:        ip,
			UserAgent: userAgent,
			LastSeen:  now,
		}
		if err := m.redis.SetActiveDevice(ctx, token, device, m.sessionDuration); err != nil {
			return nil, err
		}
		
		return result, nil
	}
	
	// Same device
	if currentDevice.DeviceID == deviceID {
		result.Allowed = true
		result.IsSameDevice = true
		
		// Update last seen
		currentDevice.LastSeen = now
		if err := m.redis.SetActiveDevice(ctx, token, currentDevice, m.sessionDuration); err != nil {
			return nil, err
		}
		
		return result, nil
	}
	
	// Different device - check timeout
	timeSinceLastSeen := now.Sub(currentDevice.LastSeen)
	if timeSinceLastSeen > m.heartbeatTimeout {
		// Old device timed out
		result.Allowed = true
		result.IsNewDevice = true
		result.TimedOut = true
		
		device := &models.DeviceInfo{
			DeviceID:  deviceID,
			IP:        ip,
			UserAgent: userAgent,
			LastSeen:  now,
		}
		if err := m.redis.SetActiveDevice(ctx, token, device, m.sessionDuration); err != nil {
			return nil, err
		}
		
		return result, nil
	}
	
	// Another device is still active
	result.Allowed = false
	result.ActiveDevice = currentDevice.DeviceID
	result.WaitTime = m.heartbeatTimeout - timeSinceLastSeen
	
	return result, nil
}

// UpdateHeartbeat updates the last seen time for a device
func (m *SessionManager) UpdateHeartbeat(ctx context.Context, token, deviceID string) error {
	device, err := m.redis.GetActiveDevice(ctx, token)
	if err != nil {
		return err
	}
	
	if device == nil {
		return nil // No device to update
	}
	
	// Only update if same device
	if device.DeviceID == deviceID {
		device.LastSeen = time.Now()
		return m.redis.SetActiveDevice(ctx, token, device, m.sessionDuration)
	}
	
	return nil
}

// GetActiveDevice returns the currently active device for a token
func (m *SessionManager) GetActiveDevice(ctx context.Context, token string) (*models.DeviceInfo, error) {
	return m.redis.GetActiveDevice(ctx, token)
}

// ForceDeviceSwitch forces a device switch (admin function)
func (m *SessionManager) ForceDeviceSwitch(ctx context.Context, token string) error {
	return m.redis.DeleteActiveDevice(ctx, token)
}
