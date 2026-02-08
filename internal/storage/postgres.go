package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/laurikarhu/stream-paywall/internal/models"
)

// PostgresStore handles all PostgreSQL database operations
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates a new PostgreSQL store
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	// Configure connection pool
	config.MaxConns = 25
	config.MinConns = 5
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Test connection
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &PostgresStore{pool: pool}, nil
}

// Close closes the database connection pool
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// GetPool returns the underlying connection pool for direct access
func (s *PostgresStore) GetPool() *pgxpool.Pool {
	return s.pool
}

// --- Stream Operations ---

// streamColumns is the list of columns for stream queries
const streamColumns = `id, slug, title, description, price_cents, start_time, end_time, status,
	COALESCE(owncast_url, ''), max_viewers, created_at,
	COALESCE(stream_key, ''), COALESCE(rtmp_port, 0), COALESCE(container_name, ''), COALESCE(container_status, 'stopped'),
	COALESCE(is_publishing, false), COALESCE(transcode_config, '[]'::jsonb)`

// scanStream scans a row into a Stream struct
func scanStream(row pgx.Row) (*models.Stream, error) {
	stream := &models.Stream{}
	err := row.Scan(
		&stream.ID,
		&stream.Slug,
		&stream.Title,
		&stream.Description,
		&stream.PriceCents,
		&stream.StartTime,
		&stream.EndTime,
		&stream.Status,
		&stream.OwncastURL,
		&stream.MaxViewers,
		&stream.CreatedAt,
		&stream.StreamKey,
		&stream.RTMPPort,
		&stream.ContainerName,
		&stream.ContainerStatus,
		&stream.IsPublishing,
		&stream.TranscodeConfig,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// scanStreamRows scans multiple rows into Stream structs
func scanStreamRows(rows pgx.Rows) ([]*models.Stream, error) {
	var streams []*models.Stream
	for rows.Next() {
		stream := &models.Stream{}
		err := rows.Scan(
			&stream.ID,
			&stream.Slug,
			&stream.Title,
			&stream.Description,
			&stream.PriceCents,
			&stream.StartTime,
			&stream.EndTime,
			&stream.Status,
			&stream.OwncastURL,
			&stream.MaxViewers,
			&stream.CreatedAt,
			&stream.StreamKey,
			&stream.RTMPPort,
			&stream.ContainerName,
			&stream.ContainerStatus,
			&stream.IsPublishing,
			&stream.TranscodeConfig,
		)
		if err != nil {
			return nil, err
		}
		streams = append(streams, stream)
	}
	return streams, rows.Err()
}

// CreateStream creates a new stream
func (s *PostgresStore) CreateStream(ctx context.Context, stream *models.Stream) error {
	transcodeConfig := stream.TranscodeConfig
	if len(transcodeConfig) == 0 {
		transcodeConfig = json.RawMessage("[]")
	}

	query := `
		INSERT INTO streams (id, slug, title, description, price_cents, start_time, end_time, status,
			owncast_url, max_viewers, created_at, stream_key, rtmp_port, container_name, container_status,
			is_publishing, transcode_config)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`
	_, err := s.pool.Exec(ctx, query,
		stream.ID,
		stream.Slug,
		stream.Title,
		stream.Description,
		stream.PriceCents,
		stream.StartTime,
		stream.EndTime,
		stream.Status,
		stream.OwncastURL,
		stream.MaxViewers,
		stream.CreatedAt,
		stream.StreamKey,
		stream.RTMPPort,
		stream.ContainerName,
		stream.ContainerStatus,
		stream.IsPublishing,
		transcodeConfig,
	)
	return err
}

// GetStreamByID retrieves a stream by its ID
func (s *PostgresStore) GetStreamByID(ctx context.Context, id uuid.UUID) (*models.Stream, error) {
	query := fmt.Sprintf("SELECT %s FROM streams WHERE id = $1", streamColumns)
	return scanStream(s.pool.QueryRow(ctx, query, id))
}

// GetStreamBySlug retrieves a stream by its slug
func (s *PostgresStore) GetStreamBySlug(ctx context.Context, slug string) (*models.Stream, error) {
	query := fmt.Sprintf("SELECT %s FROM streams WHERE slug = $1", streamColumns)
	return scanStream(s.pool.QueryRow(ctx, query, slug))
}

// GetStreamByStreamKey retrieves a stream by its stream key (for SRS webhook validation)
func (s *PostgresStore) GetStreamByStreamKey(ctx context.Context, streamKey string) (*models.Stream, error) {
	query := fmt.Sprintf("SELECT %s FROM streams WHERE stream_key = $1", streamColumns)
	return scanStream(s.pool.QueryRow(ctx, query, streamKey))
}

// ListStreams retrieves all streams
func (s *PostgresStore) ListStreams(ctx context.Context) ([]*models.Stream, error) {
	query := fmt.Sprintf("SELECT %s FROM streams ORDER BY created_at DESC", streamColumns)
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStreamRows(rows)
}

// ListActiveStreams retrieves streams that are scheduled or live
func (s *PostgresStore) ListActiveStreams(ctx context.Context) ([]*models.Stream, error) {
	query := fmt.Sprintf(`SELECT %s FROM streams
		WHERE status IN ('scheduled', 'live')
		ORDER BY start_time ASC NULLS LAST, created_at DESC`, streamColumns)
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStreamRows(rows)
}

// UpdateStream updates a stream
func (s *PostgresStore) UpdateStream(ctx context.Context, id uuid.UUID, updates *models.UpdateStreamRequest) error {
	// Build dynamic update query
	query := "UPDATE streams SET "
	args := []interface{}{}
	argNum := 1

	if updates.Title != nil {
		query += fmt.Sprintf("title = $%d, ", argNum)
		args = append(args, *updates.Title)
		argNum++
	}
	if updates.Description != nil {
		query += fmt.Sprintf("description = $%d, ", argNum)
		args = append(args, *updates.Description)
		argNum++
	}
	if updates.PriceCents != nil {
		query += fmt.Sprintf("price_cents = $%d, ", argNum)
		args = append(args, *updates.PriceCents)
		argNum++
	}
	if updates.StartTime != nil {
		query += fmt.Sprintf("start_time = $%d, ", argNum)
		args = append(args, *updates.StartTime)
		argNum++
	}
	if updates.EndTime != nil {
		query += fmt.Sprintf("end_time = $%d, ", argNum)
		args = append(args, *updates.EndTime)
		argNum++
	}
	if updates.Status != nil {
		query += fmt.Sprintf("status = $%d, ", argNum)
		args = append(args, *updates.Status)
		argNum++
	}
	if updates.MaxViewers != nil {
		query += fmt.Sprintf("max_viewers = $%d, ", argNum)
		args = append(args, *updates.MaxViewers)
		argNum++
	}
	if updates.ContainerStatus != nil {
		query += fmt.Sprintf("container_status = $%d, ", argNum)
		args = append(args, *updates.ContainerStatus)
		argNum++
	}

	if len(args) == 0 {
		return nil // Nothing to update
	}

	// Remove trailing comma and space
	query = query[:len(query)-2]
	query += fmt.Sprintf(" WHERE id = $%d", argNum)
	args = append(args, id)

	_, err := s.pool.Exec(ctx, query, args...)
	return err
}

// UpdateStreamStatus updates only the stream status
func (s *PostgresStore) UpdateStreamStatus(ctx context.Context, id uuid.UUID, status models.StreamStatus) error {
	query := "UPDATE streams SET status = $1 WHERE id = $2"
	_, err := s.pool.Exec(ctx, query, status, id)
	return err
}

// UpdateContainerStatus updates only the container status
func (s *PostgresStore) UpdateContainerStatus(ctx context.Context, id uuid.UUID, status models.ContainerStatus) error {
	query := "UPDATE streams SET container_status = $1 WHERE id = $2"
	_, err := s.pool.Exec(ctx, query, status, id)
	return err
}

// UpdateStreamPublishing updates the is_publishing flag for a stream by stream key
func (s *PostgresStore) UpdateStreamPublishing(ctx context.Context, streamKey string, isPublishing bool) error {
	query := "UPDATE streams SET is_publishing = $1 WHERE stream_key = $2"
	_, err := s.pool.Exec(ctx, query, isPublishing, streamKey)
	return err
}

// UpdateTranscodeConfig updates the transcode configuration for a stream
func (s *PostgresStore) UpdateTranscodeConfig(ctx context.Context, id uuid.UUID, config json.RawMessage) error {
	query := "UPDATE streams SET transcode_config = $1 WHERE id = $2"
	_, err := s.pool.Exec(ctx, query, config, id)
	return err
}

// DeleteStream deletes a stream
func (s *PostgresStore) DeleteStream(ctx context.Context, id uuid.UUID) error {
	query := "DELETE FROM streams WHERE id = $1"
	_, err := s.pool.Exec(ctx, query, id)
	return err
}

// --- Payment Operations ---

// CreatePayment creates a new payment record
func (s *PostgresStore) CreatePayment(ctx context.Context, payment *models.Payment) error {
	query := `
		INSERT INTO payments (id, stream_id, email, amount_cents, status, paytrail_ref, paytrail_transaction_id, access_token, token_expiry, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	// Use nil for empty access_token to avoid unique constraint violation
	// (PostgreSQL allows multiple NULLs in unique columns)
	var accessToken interface{}
	if payment.AccessToken != "" {
		accessToken = payment.AccessToken
	}

	_, err := s.pool.Exec(ctx, query,
		payment.ID,
		payment.StreamID,
		payment.Email,
		payment.AmountCents,
		payment.Status,
		payment.PaytrailRef,
		payment.PaytrailTransactionID,
		accessToken,
		payment.TokenExpiry,
		payment.CreatedAt,
	)
	return err
}

// GetPaymentByID retrieves a payment by its ID
func (s *PostgresStore) GetPaymentByID(ctx context.Context, id uuid.UUID) (*models.Payment, error) {
	query := `
		SELECT id, stream_id, email, amount_cents, status,
			COALESCE(paytrail_ref, ''), COALESCE(paytrail_transaction_id, ''),
			COALESCE(access_token, ''), token_expiry, created_at
		FROM payments WHERE id = $1
	`
	payment := &models.Payment{}
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&payment.ID,
		&payment.StreamID,
		&payment.Email,
		&payment.AmountCents,
		&payment.Status,
		&payment.PaytrailRef,
		&payment.PaytrailTransactionID,
		&payment.AccessToken,
		&payment.TokenExpiry,
		&payment.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return payment, nil
}

// GetPaymentByPaytrailRef retrieves a payment by Paytrail reference (stamp)
func (s *PostgresStore) GetPaymentByPaytrailRef(ctx context.Context, ref string) (*models.Payment, error) {
	query := `
		SELECT id, stream_id, email, amount_cents, status,
			COALESCE(paytrail_ref, ''), COALESCE(paytrail_transaction_id, ''),
			COALESCE(access_token, ''), token_expiry, created_at
		FROM payments WHERE paytrail_ref = $1
	`
	payment := &models.Payment{}
	err := s.pool.QueryRow(ctx, query, ref).Scan(
		&payment.ID,
		&payment.StreamID,
		&payment.Email,
		&payment.AmountCents,
		&payment.Status,
		&payment.PaytrailRef,
		&payment.PaytrailTransactionID,
		&payment.AccessToken,
		&payment.TokenExpiry,
		&payment.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return payment, nil
}

// GetPaymentByAccessToken retrieves a payment by access token
func (s *PostgresStore) GetPaymentByAccessToken(ctx context.Context, token string) (*models.Payment, error) {
	query := `
		SELECT id, stream_id, email, amount_cents, status,
			COALESCE(paytrail_ref, ''), COALESCE(paytrail_transaction_id, ''),
			COALESCE(access_token, ''), token_expiry, created_at
		FROM payments WHERE access_token = $1
	`
	payment := &models.Payment{}
	err := s.pool.QueryRow(ctx, query, token).Scan(
		&payment.ID,
		&payment.StreamID,
		&payment.Email,
		&payment.AmountCents,
		&payment.Status,
		&payment.PaytrailRef,
		&payment.PaytrailTransactionID,
		&payment.AccessToken,
		&payment.TokenExpiry,
		&payment.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return payment, nil
}

// GetCompletedPaymentByEmailAndStream retrieves a completed payment for recovery
func (s *PostgresStore) GetCompletedPaymentByEmailAndStream(ctx context.Context, email string, streamID uuid.UUID) (*models.Payment, error) {
	query := `
		SELECT id, stream_id, email, amount_cents, status,
			COALESCE(paytrail_ref, ''), COALESCE(paytrail_transaction_id, ''),
			COALESCE(access_token, ''), token_expiry, created_at
		FROM payments
		WHERE email = $1 AND stream_id = $2 AND status = 'completed'
		ORDER BY created_at DESC
		LIMIT 1
	`
	payment := &models.Payment{}
	err := s.pool.QueryRow(ctx, query, email, streamID).Scan(
		&payment.ID,
		&payment.StreamID,
		&payment.Email,
		&payment.AmountCents,
		&payment.Status,
		&payment.PaytrailRef,
		&payment.PaytrailTransactionID,
		&payment.AccessToken,
		&payment.TokenExpiry,
		&payment.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return payment, nil
}

// UpdatePaymentStatus updates payment status and optionally sets transaction ID and access token
func (s *PostgresStore) UpdatePaymentStatus(ctx context.Context, id uuid.UUID, status models.PaymentStatus, transactionID, accessToken string, tokenExpiry *time.Time) error {
	query := `
		UPDATE payments
		SET status = $1, paytrail_transaction_id = $2, access_token = $3, token_expiry = $4
		WHERE id = $5
	`
	// Use nil for empty access_token to avoid unique constraint violation
	var accessTokenVal interface{}
	if accessToken != "" {
		accessTokenVal = accessToken
	}

	_, err := s.pool.Exec(ctx, query, status, transactionID, accessTokenVal, tokenExpiry, id)
	return err
}

// UpdatePaymentAccessToken updates only the access token (for recovery)
func (s *PostgresStore) UpdatePaymentAccessToken(ctx context.Context, id uuid.UUID, accessToken string, tokenExpiry *time.Time) error {
	query := `UPDATE payments SET access_token = $1, token_expiry = $2 WHERE id = $3`
	_, err := s.pool.Exec(ctx, query, accessToken, tokenExpiry, id)
	return err
}

// ListPaymentsByStream retrieves all payments for a stream
func (s *PostgresStore) ListPaymentsByStream(ctx context.Context, streamID uuid.UUID) ([]*models.Payment, error) {
	query := `
		SELECT id, stream_id, email, amount_cents, status,
			COALESCE(paytrail_ref, ''), COALESCE(paytrail_transaction_id, ''),
			COALESCE(access_token, ''), token_expiry, created_at
		FROM payments
		WHERE stream_id = $1
		ORDER BY created_at DESC
	`
	rows, err := s.pool.Query(ctx, query, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var payments []*models.Payment
	for rows.Next() {
		payment := &models.Payment{}
		err := rows.Scan(
			&payment.ID,
			&payment.StreamID,
			&payment.Email,
			&payment.AmountCents,
			&payment.Status,
			&payment.PaytrailRef,
			&payment.PaytrailTransactionID,
			&payment.AccessToken,
			&payment.TokenExpiry,
			&payment.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		payments = append(payments, payment)
	}
	return payments, rows.Err()
}

// CountCompletedPaymentsByStream counts completed payments for a stream
func (s *PostgresStore) CountCompletedPaymentsByStream(ctx context.Context, streamID uuid.UUID) (int, error) {
	query := "SELECT COUNT(*) FROM payments WHERE stream_id = $1 AND status = 'completed'"
	var count int
	err := s.pool.QueryRow(ctx, query, streamID).Scan(&count)
	return count, err
}

// --- Whitelist Operations ---

// AddWhitelistEntry adds an email to a stream's whitelist
func (s *PostgresStore) AddWhitelistEntry(ctx context.Context, streamID uuid.UUID, email, notes string) (*models.WhitelistEntry, error) {
	entry := &models.WhitelistEntry{
		ID:        uuid.New(),
		StreamID:  streamID,
		Email:     email,
		Notes:     notes,
		CreatedAt: time.Now(),
	}

	query := `
		INSERT INTO stream_whitelist (id, stream_id, email, notes, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (stream_id, email) DO UPDATE SET notes = $4
		RETURNING id, created_at
	`
	err := s.pool.QueryRow(ctx, query, entry.ID, entry.StreamID, entry.Email, entry.Notes, entry.CreatedAt).
		Scan(&entry.ID, &entry.CreatedAt)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// RemoveWhitelistEntry removes an email from a stream's whitelist
func (s *PostgresStore) RemoveWhitelistEntry(ctx context.Context, streamID uuid.UUID, email string) error {
	query := `DELETE FROM stream_whitelist WHERE stream_id = $1 AND email = $2`
	_, err := s.pool.Exec(ctx, query, streamID, email)
	return err
}

// GetWhitelistByStream returns all whitelisted emails for a stream
func (s *PostgresStore) GetWhitelistByStream(ctx context.Context, streamID uuid.UUID) ([]*models.WhitelistEntry, error) {
	query := `
		SELECT id, stream_id, email, COALESCE(notes, ''), created_at
		FROM stream_whitelist
		WHERE stream_id = $1
		ORDER BY created_at DESC
	`
	rows, err := s.pool.Query(ctx, query, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*models.WhitelistEntry
	for rows.Next() {
		entry := &models.WhitelistEntry{}
		err := rows.Scan(&entry.ID, &entry.StreamID, &entry.Email, &entry.Notes, &entry.CreatedAt)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// IsEmailWhitelisted checks if an email is whitelisted for a stream
func (s *PostgresStore) IsEmailWhitelisted(ctx context.Context, streamID uuid.UUID, email string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM stream_whitelist WHERE stream_id = $1 AND email = $2)`
	var exists bool
	err := s.pool.QueryRow(ctx, query, streamID, email).Scan(&exists)
	return exists, err
}

// GetWhitelistEntry gets a specific whitelist entry
func (s *PostgresStore) GetWhitelistEntry(ctx context.Context, streamID uuid.UUID, email string) (*models.WhitelistEntry, error) {
	query := `
		SELECT id, stream_id, email, COALESCE(notes, ''), created_at
		FROM stream_whitelist
		WHERE stream_id = $1 AND email = $2
	`
	entry := &models.WhitelistEntry{}
	err := s.pool.QueryRow(ctx, query, streamID, email).Scan(
		&entry.ID, &entry.StreamID, &entry.Email, &entry.Notes, &entry.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// PaymentStats holds aggregated payment statistics
type PaymentStats struct {
	TotalPayments     int
	CompletedPayments int
	TotalRevenueCents int
}

// GetPaymentStatsAggregate returns aggregated payment stats in a single query
// This avoids the N+1 query problem of fetching payments per stream
func (s *PostgresStore) GetPaymentStatsAggregate(ctx context.Context) (*PaymentStats, error) {
	query := `
		SELECT
			COUNT(*) as total_payments,
			COUNT(*) FILTER (WHERE status = 'completed') as completed_payments,
			COALESCE(SUM(amount_cents) FILTER (WHERE status = 'completed'), 0) as total_revenue
		FROM payments
	`
	stats := &PaymentStats{}
	err := s.pool.QueryRow(ctx, query).Scan(
		&stats.TotalPayments,
		&stats.CompletedPayments,
		&stats.TotalRevenueCents,
	)
	if err != nil {
		return nil, err
	}
	return stats, nil
}

// GetRecentCompletedPayments returns the most recent completed payments with stream titles
// Uses a single query with JOIN instead of N+1 queries
func (s *PostgresStore) GetRecentCompletedPayments(ctx context.Context, limit int) ([]*models.Payment, map[uuid.UUID]string, error) {
	query := `
		SELECT p.id, p.stream_id, p.email, p.amount_cents, p.status,
		       p.paytrail_ref, p.paytrail_transaction_id, p.access_token,
		       p.token_expiry, p.created_at, s.title
		FROM payments p
		JOIN streams s ON p.stream_id = s.id
		WHERE p.status = 'completed'
		ORDER BY p.created_at DESC
		LIMIT $1
	`
	rows, err := s.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var payments []*models.Payment
	streamTitles := make(map[uuid.UUID]string)

	for rows.Next() {
		p := &models.Payment{}
		var streamTitle string
		var paytrailRef, paytrailTxID, accessToken *string
		var tokenExpiry *time.Time

		err := rows.Scan(
			&p.ID, &p.StreamID, &p.Email, &p.AmountCents, &p.Status,
			&paytrailRef, &paytrailTxID, &accessToken,
			&tokenExpiry, &p.CreatedAt, &streamTitle,
		)
		if err != nil {
			return nil, nil, err
		}

		if paytrailRef != nil {
			p.PaytrailRef = *paytrailRef
		}
		if paytrailTxID != nil {
			p.PaytrailTransactionID = *paytrailTxID
		}
		if accessToken != nil {
			p.AccessToken = *accessToken
		}
		if tokenExpiry != nil {
			p.TokenExpiry = tokenExpiry
		}

		payments = append(payments, p)
		streamTitles[p.StreamID] = streamTitle
	}

	return payments, streamTitles, nil
}
