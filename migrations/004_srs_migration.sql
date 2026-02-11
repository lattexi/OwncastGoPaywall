-- SRS Migration: Replace per-stream Owncast containers with single SRS instance

-- Track whether a stream is currently publishing via SRS
ALTER TABLE streams ADD COLUMN IF NOT EXISTS is_publishing BOOLEAN DEFAULT false;

-- Store transcoding configuration as JSONB
ALTER TABLE streams ADD COLUMN IF NOT EXISTS transcode_config JSONB DEFAULT '[]';

-- All streams now share rtmp_port 19350, drop UNIQUE constraint
ALTER TABLE streams DROP CONSTRAINT IF EXISTS streams_rtmp_port_key;

-- Container status default (SRS is managed by docker-compose, not Go)
ALTER TABLE streams ALTER COLUMN container_status SET DEFAULT 'stopped';
