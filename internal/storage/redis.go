package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/laurikarhu/stream-paywall/internal/models"
	"github.com/redis/go-redis/v9"
)

// RedisStore handles all Redis operations for sessions and caching
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore creates a new Redis store
func NewRedisStore(ctx context.Context, redisURL string) (*RedisStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %w", err)
	}

	client := redis.NewClient(opts)

	// Test connection
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping Redis: %w", err)
	}

	return &RedisStore{client: client}, nil
}

// Close closes the Redis connection
func (s *RedisStore) Close() error {
	return s.client.Close()
}

// --- Session Operations ---

// Key patterns
const (
	sessionKeyPrefix    = "session:"
	deviceKeyPrefix     = "device:"
	rateLimitKeyPrefix  = "ratelimit:"
	viewerCountPrefix   = "viewers:"
)

// SessionData represents the data stored in a session
type SessionData struct {
	Token     string    `json:"token"`
	StreamID  string    `json:"stream_id"`
	Email     string    `json:"email"`
	PaymentID string    `json:"payment_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SetSession stores session data with TTL
func (s *RedisStore) SetSession(ctx context.Context, token string, data *SessionData, ttl time.Duration) error {
	key := sessionKeyPrefix + token
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal session data: %w", err)
	}
	return s.client.Set(ctx, key, jsonData, ttl).Err()
}

// GetSession retrieves session data
func (s *RedisStore) GetSession(ctx context.Context, token string) (*SessionData, error) {
	key := sessionKeyPrefix + token
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var session SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session data: %w", err)
	}
	return &session, nil
}

// DeleteSession removes a session
func (s *RedisStore) DeleteSession(ctx context.Context, token string) error {
	key := sessionKeyPrefix + token
	return s.client.Del(ctx, key).Err()
}

// RefreshSession extends the session TTL
func (s *RedisStore) RefreshSession(ctx context.Context, token string, ttl time.Duration) error {
	key := sessionKeyPrefix + token
	return s.client.Expire(ctx, key, ttl).Err()
}

// --- Device Tracking ---

// SetActiveDevice sets the active device for a token
func (s *RedisStore) SetActiveDevice(ctx context.Context, token string, device *models.DeviceInfo, ttl time.Duration) error {
	key := deviceKeyPrefix + token
	jsonData, err := json.Marshal(device)
	if err != nil {
		return fmt.Errorf("failed to marshal device info: %w", err)
	}
	return s.client.Set(ctx, key, jsonData, ttl).Err()
}

// GetActiveDevice retrieves the active device for a token
func (s *RedisStore) GetActiveDevice(ctx context.Context, token string) (*models.DeviceInfo, error) {
	key := deviceKeyPrefix + token
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var device models.DeviceInfo
	if err := json.Unmarshal(data, &device); err != nil {
		return nil, fmt.Errorf("failed to unmarshal device info: %w", err)
	}
	return &device, nil
}

// UpdateDeviceLastSeen updates only the LastSeen timestamp
func (s *RedisStore) UpdateDeviceLastSeen(ctx context.Context, token string, ttl time.Duration) error {
	device, err := s.GetActiveDevice(ctx, token)
	if err != nil {
		return err
	}
	if device == nil {
		return nil
	}
	device.LastSeen = time.Now()
	return s.SetActiveDevice(ctx, token, device, ttl)
}

// DeleteActiveDevice removes the active device binding
func (s *RedisStore) DeleteActiveDevice(ctx context.Context, token string) error {
	key := deviceKeyPrefix + token
	return s.client.Del(ctx, key).Err()
}

// --- Rate Limiting ---

// CheckAndIncrementRateLimit checks if rate limit is exceeded and increments counter
// Returns true if the request should be allowed, false if rate limited
func (s *RedisStore) CheckAndIncrementRateLimit(ctx context.Context, keyType, identifier string, limit int, window time.Duration) (bool, error) {
	key := fmt.Sprintf("%s%s:%s", rateLimitKeyPrefix, keyType, identifier)

	// Use a Lua script for atomic check-and-increment
	script := redis.NewScript(`
		local current = redis.call('GET', KEYS[1])
		if current and tonumber(current) >= tonumber(ARGV[1]) then
			return 0
		end
		local result = redis.call('INCR', KEYS[1])
		if result == 1 then
			redis.call('EXPIRE', KEYS[1], ARGV[2])
		end
		return 1
	`)

	result, err := script.Run(ctx, s.client, []string{key}, limit, int(window.Seconds())).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// hashEmail hashes an email for use in rate limit keys
func hashEmail(email string) string {
	h := sha256.Sum256([]byte(email))
	return hex.EncodeToString(h[:16]) // Use first 16 bytes
}

// CheckRecoveryRateLimit checks rate limits for token recovery
// Returns true if allowed, false if rate limited
func (s *RedisStore) CheckRecoveryRateLimit(ctx context.Context, email, ip string, emailLimit, ipLimit int) (bool, error) {
	emailHash := hashEmail(email)

	// Check email rate limit
	emailAllowed, err := s.CheckAndIncrementRateLimit(ctx, "recover:email:", emailHash, emailLimit, time.Hour)
	if err != nil {
		return false, err
	}
	if !emailAllowed {
		return false, nil
	}

	// Check IP rate limit
	ipAllowed, err := s.CheckAndIncrementRateLimit(ctx, "recover:ip:", ip, ipLimit, time.Hour)
	if err != nil {
		return false, err
	}
	return ipAllowed, nil
}

// --- Viewer Counting ---

// IncrementViewerCount increments the viewer count for a stream
func (s *RedisStore) IncrementViewerCount(ctx context.Context, streamID uuid.UUID) error {
	key := viewerCountPrefix + streamID.String()
	return s.client.Incr(ctx, key).Err()
}

// DecrementViewerCount decrements the viewer count for a stream
func (s *RedisStore) DecrementViewerCount(ctx context.Context, streamID uuid.UUID) error {
	key := viewerCountPrefix + streamID.String()
	result, err := s.client.Decr(ctx, key).Result()
	if err != nil {
		return err
	}
	// Ensure we don't go negative
	if result < 0 {
		return s.client.Set(ctx, key, 0, 0).Err()
	}
	return nil
}

// GetViewerCount gets the current viewer count for a stream
func (s *RedisStore) GetViewerCount(ctx context.Context, streamID uuid.UUID) (int, error) {
	key := viewerCountPrefix + streamID.String()
	result, err := s.client.Get(ctx, key).Int()
	if err == redis.Nil {
		return 0, nil
	}
	return result, err
}

// SetViewerCount sets the viewer count directly (for initialization or correction)
func (s *RedisStore) SetViewerCount(ctx context.Context, streamID uuid.UUID, count int) error {
	key := viewerCountPrefix + streamID.String()
	return s.client.Set(ctx, key, count, 0).Err()
}

// --- Active Session Tracking for Viewer Counts ---

// TrackActiveSession adds a session to the active sessions set for a stream
func (s *RedisStore) TrackActiveSession(ctx context.Context, streamID uuid.UUID, token string, ttl time.Duration) error {
	key := "active_sessions:" + streamID.String()
	member := redis.Z{
		Score:  float64(time.Now().Add(ttl).Unix()),
		Member: token,
	}
	return s.client.ZAdd(ctx, key, member).Err()
}

// RemoveActiveSession removes a session from the active sessions set
func (s *RedisStore) RemoveActiveSession(ctx context.Context, streamID uuid.UUID, token string) error {
	key := "active_sessions:" + streamID.String()
	return s.client.ZRem(ctx, key, token).Err()
}

// CountActiveSessions counts active sessions for a stream (removes expired ones)
func (s *RedisStore) CountActiveSessions(ctx context.Context, streamID uuid.UUID) (int64, error) {
	key := "active_sessions:" + streamID.String()
	now := float64(time.Now().Unix())

	// Remove expired sessions
	if err := s.client.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%f", now)).Err(); err != nil {
		return 0, err
	}

	// Count remaining
	return s.client.ZCard(ctx, key).Result()
}
