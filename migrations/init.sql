-- Stream Paywall Database Initialization Script
-- This script creates all tables for a fresh installation
-- Run: docker compose exec -T postgres psql -U paywall -d paywall < migrations/init.sql

-- Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ============================================
-- STREAMS TABLE
-- ============================================
CREATE TABLE IF NOT EXISTS streams (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug VARCHAR(100) UNIQUE NOT NULL,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    price_cents INTEGER NOT NULL CHECK (price_cents >= 0),
    start_time TIMESTAMPTZ,
    end_time TIMESTAMPTZ,
    status VARCHAR(20) NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'live', 'ended')),
    owncast_url VARCHAR(500),  -- Auto-generated based on container
    max_viewers INTEGER DEFAULT 0 CHECK (max_viewers >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Dynamic Owncast container fields
    stream_key VARCHAR(64),                    -- Auto-generated OBS stream key
    rtmp_port INTEGER UNIQUE,                  -- Assigned RTMP port (19350+)
    container_name VARCHAR(100) UNIQUE,        -- Docker container name
    container_status VARCHAR(20) DEFAULT 'stopped' CHECK (container_status IN ('stopped', 'starting', 'running', 'stopping', 'error')),
    
    -- Constraints
    CONSTRAINT valid_time_range CHECK (end_time IS NULL OR start_time IS NULL OR end_time > start_time)
);

CREATE INDEX IF NOT EXISTS idx_streams_slug ON streams(slug);
CREATE INDEX IF NOT EXISTS idx_streams_status ON streams(status);
CREATE INDEX IF NOT EXISTS idx_streams_start_time ON streams(start_time);
CREATE INDEX IF NOT EXISTS idx_streams_rtmp_port ON streams(rtmp_port);

COMMENT ON TABLE streams IS 'Paywall-protected video streams';
COMMENT ON COLUMN streams.slug IS 'URL-friendly unique identifier';
COMMENT ON COLUMN streams.price_cents IS 'Price in cents (990 = 9.90€)';
COMMENT ON COLUMN streams.owncast_url IS 'Internal Owncast URL - auto-generated from container name';
COMMENT ON COLUMN streams.max_viewers IS '0 means unlimited viewers';
COMMENT ON COLUMN streams.stream_key IS 'Auto-generated stream key for OBS';
COMMENT ON COLUMN streams.rtmp_port IS 'Assigned RTMP port for this stream (19350+)';
COMMENT ON COLUMN streams.container_name IS 'Docker container name for this stream';
COMMENT ON COLUMN streams.container_status IS 'Current status of the Owncast container';

-- ============================================
-- PAYMENTS TABLE
-- ============================================
CREATE TABLE IF NOT EXISTS payments (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    stream_id UUID NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    email VARCHAR(255) NOT NULL,
    amount_cents INTEGER NOT NULL CHECK (amount_cents >= 0),
    status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'failed', 'refunded')),
    paytrail_ref VARCHAR(100),           -- Unique stamp sent to Paytrail, or "whitelist" for free access
    paytrail_transaction_id VARCHAR(100),
    access_token VARCHAR(64) UNIQUE,
    token_expiry TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payments_stream_id ON payments(stream_id);
CREATE INDEX IF NOT EXISTS idx_payments_email ON payments(email);
CREATE INDEX IF NOT EXISTS idx_payments_access_token ON payments(access_token);
CREATE INDEX IF NOT EXISTS idx_payments_paytrail_ref ON payments(paytrail_ref);
CREATE INDEX IF NOT EXISTS idx_payments_status ON payments(status);
CREATE INDEX IF NOT EXISTS idx_payments_email_stream_status ON payments(email, stream_id, status);

COMMENT ON TABLE payments IS 'Payment records for stream access';
COMMENT ON COLUMN payments.paytrail_ref IS 'Unique stamp sent to Paytrail, or "whitelist" for free access';
COMMENT ON COLUMN payments.paytrail_transaction_id IS 'Transaction ID returned by Paytrail';
COMMENT ON COLUMN payments.access_token IS 'Token for accessing the stream';

-- ============================================
-- ADMIN USERS TABLE
-- ============================================
CREATE TABLE IF NOT EXISTS admin_users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    username VARCHAR(50) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,  -- bcrypt hash
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_admin_users_username ON admin_users(username);

COMMENT ON TABLE admin_users IS 'Admin users for the dashboard';
COMMENT ON COLUMN admin_users.password_hash IS 'bcrypt hashed password';

-- ============================================
-- STREAM WHITELIST TABLE
-- ============================================
CREATE TABLE IF NOT EXISTS stream_whitelist (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    stream_id UUID NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    email VARCHAR(255) NOT NULL,
    notes TEXT,  -- Admin notes (e.g., "VIP customer", "Press")
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Each email can only be whitelisted once per stream
    UNIQUE(stream_id, email)
);

CREATE INDEX IF NOT EXISTS idx_whitelist_stream_email ON stream_whitelist(stream_id, email);
CREATE INDEX IF NOT EXISTS idx_whitelist_email ON stream_whitelist(email);

COMMENT ON TABLE stream_whitelist IS 'Whitelisted emails that can access streams without payment';
COMMENT ON COLUMN stream_whitelist.notes IS 'Optional admin notes explaining why this email is whitelisted';

-- ============================================
-- DONE
-- ============================================
-- Database initialization complete!
-- An admin user will be auto-created on first startup using ADMIN_INITIAL_USER and ADMIN_INITIAL_PASSWORD env vars.
