-- Stream whitelist for granting free access to specific emails
-- Run: docker compose exec -T postgres psql -U paywall -d paywall < migrations/003_whitelist.sql

CREATE TABLE IF NOT EXISTS stream_whitelist (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    stream_id UUID NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    email VARCHAR(255) NOT NULL,
    notes TEXT,  -- Admin notes (e.g., "VIP customer", "Press")
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Each email can only be whitelisted once per stream
    UNIQUE(stream_id, email)
);

-- Index for fast lookups
CREATE INDEX IF NOT EXISTS idx_whitelist_stream_email ON stream_whitelist(stream_id, email);
CREATE INDEX IF NOT EXISTS idx_whitelist_email ON stream_whitelist(email);

COMMENT ON TABLE stream_whitelist IS 'Whitelisted emails that can access streams without payment';
COMMENT ON COLUMN stream_whitelist.notes IS 'Optional admin notes explaining why this email is whitelisted';
