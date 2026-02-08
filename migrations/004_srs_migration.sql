-- Migration 004: Replace Owncast with SRS streaming server
-- All streams now share a single SRS container instead of individual Owncast containers

ALTER TABLE streams ADD COLUMN IF NOT EXISTS transcode_config JSONB DEFAULT '[]'::jsonb;
ALTER TABLE streams ADD COLUMN IF NOT EXISTS is_publishing BOOLEAN DEFAULT false;

-- Drop per-stream constraints (all streams share single SRS)
ALTER TABLE streams DROP CONSTRAINT IF EXISTS streams_container_name_key;
ALTER TABLE streams DROP CONSTRAINT IF EXISTS streams_rtmp_port_key;
DROP INDEX IF EXISTS idx_streams_rtmp_port;

-- Set all ports to single shared port
UPDATE streams SET rtmp_port = 19350;
