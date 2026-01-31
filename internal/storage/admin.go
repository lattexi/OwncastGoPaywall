package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

// AdminUser represents an admin user
type AdminUser struct {
	ID           uuid.UUID  `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLogin    *time.Time `json:"last_login,omitempty"`
}

// CreateAdminUser creates a new admin user with a hashed password
func (s *PostgresStore) CreateAdminUser(ctx context.Context, username, password string) (*AdminUser, error) {
	// Hash password with bcrypt
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, err
	}

	user := &AdminUser{
		ID:           uuid.New(),
		Username:     username,
		PasswordHash: string(hash),
		CreatedAt:    time.Now(),
	}

	query := `
		INSERT INTO admin_users (id, username, password_hash, created_at)
		VALUES ($1, $2, $3, $4)
	`
	_, err = s.pool.Exec(ctx, query, user.ID, user.Username, user.PasswordHash, user.CreatedAt)
	if err != nil {
		return nil, err
	}

	return user, nil
}

// GetAdminUserByUsername retrieves an admin user by username
func (s *PostgresStore) GetAdminUserByUsername(ctx context.Context, username string) (*AdminUser, error) {
	query := `
		SELECT id, username, password_hash, created_at, last_login
		FROM admin_users WHERE username = $1
	`
	user := &AdminUser{}
	err := s.pool.QueryRow(ctx, query, username).Scan(
		&user.ID,
		&user.Username,
		&user.PasswordHash,
		&user.CreatedAt,
		&user.LastLogin,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetAdminUserByID retrieves an admin user by ID
func (s *PostgresStore) GetAdminUserByID(ctx context.Context, id uuid.UUID) (*AdminUser, error) {
	query := `
		SELECT id, username, password_hash, created_at, last_login
		FROM admin_users WHERE id = $1
	`
	user := &AdminUser{}
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&user.ID,
		&user.Username,
		&user.PasswordHash,
		&user.CreatedAt,
		&user.LastLogin,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

// VerifyAdminPassword verifies a password against the stored hash
func (s *PostgresStore) VerifyAdminPassword(ctx context.Context, username, password string) (*AdminUser, bool) {
	user, err := s.GetAdminUserByUsername(ctx, username)
	if err != nil || user == nil {
		// Perform a dummy bcrypt comparison to prevent timing attacks
		bcrypt.CompareHashAndPassword([]byte("$2a$12$dummy.hash.for.timing.attack.prevention"), []byte(password))
		return nil, false
	}

	err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
	if err != nil {
		return nil, false
	}

	return user, true
}

// UpdateAdminLastLogin updates the last login timestamp
func (s *PostgresStore) UpdateAdminLastLogin(ctx context.Context, id uuid.UUID) error {
	query := `UPDATE admin_users SET last_login = $1 WHERE id = $2`
	_, err := s.pool.Exec(ctx, query, time.Now(), id)
	return err
}

// ListAdminUsers lists all admin users
func (s *PostgresStore) ListAdminUsers(ctx context.Context) ([]*AdminUser, error) {
	query := `
		SELECT id, username, password_hash, created_at, last_login
		FROM admin_users ORDER BY created_at ASC
	`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*AdminUser
	for rows.Next() {
		user := &AdminUser{}
		err := rows.Scan(
			&user.ID,
			&user.Username,
			&user.PasswordHash,
			&user.CreatedAt,
			&user.LastLogin,
		)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// CountAdminUsers returns the number of admin users
func (s *PostgresStore) CountAdminUsers(ctx context.Context) (int, error) {
	query := `SELECT COUNT(*) FROM admin_users`
	var count int
	err := s.pool.QueryRow(ctx, query).Scan(&count)
	return count, err
}

// DeleteAdminUser deletes an admin user
func (s *PostgresStore) DeleteAdminUser(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM admin_users WHERE id = $1`
	_, err := s.pool.Exec(ctx, query, id)
	return err
}

// UpdateAdminPassword updates an admin user's password
func (s *PostgresStore) UpdateAdminPassword(ctx context.Context, id uuid.UUID, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return err
	}

	query := `UPDATE admin_users SET password_hash = $1 WHERE id = $2`
	_, err = s.pool.Exec(ctx, query, string(hash), id)
	return err
}

// --- Admin Session Storage in Redis ---

// AdminSession represents an admin session
type AdminSession struct {
	SessionID string    `json:"session_id"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

const adminSessionPrefix = "admin_session:"

// SetAdminSession stores an admin session in Redis
func (s *RedisStore) SetAdminSession(ctx context.Context, session *AdminSession, ttl time.Duration) error {
	key := adminSessionPrefix + session.SessionID
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, key, data, ttl).Err()
}

// GetAdminSession retrieves an admin session from Redis
func (s *RedisStore) GetAdminSession(ctx context.Context, sessionID string) (*AdminSession, error) {
	key := adminSessionPrefix + sessionID
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var session AdminSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// DeleteAdminSession removes an admin session from Redis
func (s *RedisStore) DeleteAdminSession(ctx context.Context, sessionID string) error {
	key := adminSessionPrefix + sessionID
	return s.client.Del(ctx, key).Err()
}

// RefreshAdminSession extends the session TTL
func (s *RedisStore) RefreshAdminSession(ctx context.Context, sessionID string, ttl time.Duration) error {
	key := adminSessionPrefix + sessionID
	return s.client.Expire(ctx, key, ttl).Err()
}

// --- Login Rate Limiting ---

// CheckAdminLoginRateLimit checks if login attempts are rate limited
// Returns true if allowed, false if rate limited
func (s *RedisStore) CheckAdminLoginRateLimit(ctx context.Context, username, ip string) (bool, error) {
	// Check username rate limit (5 per 15 minutes)
	usernameAllowed, err := s.CheckAndIncrementRateLimit(ctx, "admin_login:user:", username, 5, 15*time.Minute)
	if err != nil {
		return false, err
	}
	if !usernameAllowed {
		return false, nil
	}

	// Check IP rate limit (10 per 15 minutes)
	ipAllowed, err := s.CheckAndIncrementRateLimit(ctx, "admin_login:ip:", ip, 10, 15*time.Minute)
	if err != nil {
		return false, err
	}
	return ipAllowed, nil
}
