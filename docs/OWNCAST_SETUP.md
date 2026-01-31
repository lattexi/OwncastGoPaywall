# Owncast Setup Guide

This guide covers setting up Owncast for use with the Stream Paywall.

## Overview

Owncast runs on the internal Docker network and is only accessible through the paywall proxy. This ensures streams cannot be accessed without payment.

## Network Isolation

The `docker-compose.yml` configures Owncast on an internal-only network:

```yaml
owncast:
  image: owncast/owncast:latest
  # NO ports mapping - only accessible from internal network
  networks:
    - internal
```

The paywall server proxies all HLS requests to Owncast:

```
Client → Paywall (public:3000) → Owncast (internal:8080)
```

## Configuration

### 1. Access Owncast Admin

Since Owncast isn't exposed publicly, access admin via SSH tunnel:

```bash
# SSH tunnel to Owncast admin
ssh -L 8080:localhost:8080 yourserver

# Or use docker exec
docker exec -it owncast /bin/sh
```

Alternatively, temporarily expose the port for initial setup:

```yaml
# docker-compose.override.yml (REMOVE AFTER SETUP!)
services:
  owncast:
    ports:
      - "127.0.0.1:8080:8080"  # Only accessible from localhost
```

### 2. Configure Stream Key

1. Access admin at `http://localhost:8080/admin`
2. Go to **Configuration** → **Server Setup**
3. Set a secure **Stream Key**
4. Note this key for OBS/streaming software

### 3. Video Quality Settings

Recommended settings for different use cases:

#### Standard Quality (720p)
```yaml
Video Bitrate: 2500 kbps
Audio Bitrate: 128 kbps
Resolution: 1280x720
Framerate: 30
```

#### High Quality (1080p)
```yaml
Video Bitrate: 4500 kbps
Audio Bitrate: 192 kbps
Resolution: 1920x1080
Framerate: 30
```

#### Multiple Quality Variants
Configure in Owncast admin for adaptive streaming:
- 1080p @ 4500 kbps
- 720p @ 2500 kbps
- 480p @ 1000 kbps

### 4. Disable Public Features

Since all access goes through the paywall:

1. **Disable chat** (or implement separately)
2. **Disable public directory listing**
3. **Disable embedded player** (viewers use paywall player)

### 5. HLS Configuration

Owncast serves HLS at: `http://owncast:8080/hls/stream.m3u8`

Configure in Owncast admin:
- **Segment Duration**: 2-4 seconds
- **Playlist Length**: 5-10 segments

## Streaming to Owncast

### OBS Studio Configuration

1. **Settings** → **Stream**
2. **Service**: Custom
3. **Server**: `rtmp://your-server-ip:1935/live`
4. **Stream Key**: Your configured stream key

### Going Live

1. Start streaming from OBS
2. Set stream status to "live" via admin API:
   ```bash
   curl -X PATCH https://stream.yourdomain.com/admin/streams/{id}/status \
     -H "X-Admin-Key: your-key" \
     -d '{"status": "live"}'
   ```

## Troubleshooting

### Stream Not Appearing

1. Check Owncast status:
   ```bash
   docker compose logs owncast
   ```

2. Verify Owncast is receiving stream:
   ```bash
   curl http://localhost:8080/api/status
   ```

3. Check internal network connectivity:
   ```bash
   docker exec stream-paywall wget -q -O- http://owncast:8080/api/status
   ```

### High Latency

1. Reduce segment duration in Owncast
2. Enable low-latency mode in player
3. Check network between services

### Quality Issues

1. Verify source stream quality
2. Check Owncast transcoding settings
3. Monitor server CPU/memory

## Resource Requirements

| Viewers | CPU | RAM | Bandwidth |
|---------|-----|-----|-----------|
| 10 | 2 cores | 2GB | 50 Mbps |
| 100 | 4 cores | 4GB | 500 Mbps |
| 500 | 8 cores | 8GB | 2.5 Gbps |
| 1000 | 16 cores | 16GB | 5 Gbps |

*Assumes 720p @ 2500 kbps per viewer*

## Persistence

Owncast data is stored in a Docker volume:

```yaml
volumes:
  - owncast-data:/app/data
```

This includes:
- Configuration
- Stream recordings (if enabled)
- Chat history
- Admin settings

## References

- [Owncast Documentation](https://owncast.online/docs/)
- [Owncast Configuration](https://owncast.online/docs/configuration/)
- [OBS Studio Setup](https://owncast.online/docs/broadcasting/)
