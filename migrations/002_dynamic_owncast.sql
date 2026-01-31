-- Migration for dynamic Owncast container support
-- Run this if you already have the initial schema

-- Add new columns to streams table
ALTER TABLE streams ADD COLUMN IF NOT EXISTS stream_key VARCHAR(64);
ALTER TABLE streams ADD COLUMN IF NOT EXISTS rtmp_port INTEGER UNIQUE;
ALTER TABLE streams ADD COLUMN IF NOT EXISTS container_name VARCHAR(100) UNIQUE;
ALTER TABLE streams ADD COLUMN IF NOT EXISTS container_status VARCHAR(20) DEFAULT 'stopped';

-- Make owncast_url nullable (it's now auto-generated)
ALTER TABLE streams ALTER COLUMN owncast_url DROP NOT NULL;

-- Add constraint for container_status
ALTER TABLE streams DROP CONSTRAINT IF EXISTS streams_container_status_check;
ALTER TABLE streams ADD CONSTRAINT streams_container_status_check 
    CHECK (container_status IN ('stopped', 'starting', 'running', 'stopping', 'error'));

-- Add index for port lookup
CREATE INDEX IF NOT EXISTS idx_streams_rtmp_port ON streams(rtmp_port);

-- Comments
COMMENT ON COLUMN streams.stream_key IS 'Auto-generated stream key for OBS';
COMMENT ON COLUMN streams.rtmp_port IS 'Assigned RTMP port for this stream (19350+)';
COMMENT ON COLUMN streams.container_name IS 'Docker container name for this stream';
COMMENT ON COLUMN streams.container_status IS 'Current status of the Owncast container';
