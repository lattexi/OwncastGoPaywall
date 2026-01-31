# Stream Paywall

A production-ready Go server that acts as a paywall proxy for Owncast video streams, integrating Paytrail payments (Finnish payment provider) with secure stream access control.

## Features

- **Paytrail Integration**: Finnish payment provider support with HMAC signature verification
- **Dynamic Owncast Containers**: Auto-provisioned Owncast instances per stream with unique stream keys
- **HLS Stream Proxying**: Proxies Owncast streams while hiding the backend URL
- **Signed URLs**: Cryptographically signed, time-limited segment URLs
- **Token Recovery**: Users can recover their access using their purchase email
- **Email Whitelist**: Grant free access to specific emails (VIPs, press, etc.)
- **Admin Web UI**: Full-featured dashboard for stream and payment management
- **Real-time Viewer Counts**: Track active viewers per stream

## Architecture

```
Internet                              Internal Network
                                      
┌──────────┐    HTTPS    ┌───────────────────┐    HTTP     ┌──────────────┐
│  Client  │────────────▶│   Go Server       │────────────▶│ Owncast      │
│(browser) │◀────────────│   (paywall)       │◀────────────│ Containers   │
└──────────┘             │                   │             │ (dynamic)    │
                         │ • Paytrail payments│            └──────────────┘
     ┌──────────┐        │ • Authentication   │                   │
     │   OBS    │────────│ • HLS proxying     │───────────────────┘
     │(streamer)│  RTMP  │ • Container mgmt   │            RTMP (19350+)
     └──────────┘        └───────────────────┘
```

**Critical Security**: The client never sees or can discover the Owncast server's real URL. All stream requests go through the Go proxy.

## Quick Start

### Prerequisites

- Go 1.24+
- Docker & Docker Compose
- PostgreSQL 16+
- Redis 7+

### Development Setup

1. Clone the repository:
   ```bash
   git clone https://github.com/youruser/stream-paywall.git
   cd stream-paywall
   ```

2. Copy environment file:
   ```bash
   cp .env.example .env
   # Edit .env with your configuration
   ```

3. Start all services with Docker:
   ```bash
   docker compose up -d
   ```

4. Initialize database (first time only):
   ```bash
   docker compose exec -T postgres psql -U paywall -d paywall < migrations/init.sql
   ```

5. Access the admin panel:
   - URL: http://localhost:3000/admin
   - Default credentials: `admin` / `admin` (change after first login!)

### Production Deployment

```bash
# Configure environment
cp .env.example .env
# Edit .env with production values (especially secrets!)

# Start all services
docker compose up -d

# View logs
docker compose logs -f paywall
```

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `BASE_URL` | Public URL of the server | `http://localhost:3000` |
| `PORT` | Server port | `3000` |
| `PAYTRAIL_MERCHANT_ID` | Paytrail merchant ID | `375917` (test) |
| `PAYTRAIL_SECRET_KEY` | Paytrail secret key | `SAIPPUAKAUPPIAS` (test) |
| `SIGNING_SECRET` | Secret for URL signing | **Required** |
| `ADMIN_API_KEY` | API key for admin endpoints | **Required** |
| `ADMIN_INITIAL_USER` | Initial admin username | `admin` |
| `ADMIN_INITIAL_PASSWORD` | Initial admin password | `admin` |
| `DATABASE_URL` | PostgreSQL connection string | - |
| `REDIS_URL` | Redis connection string | - |
| `SESSION_DURATION` | Access token validity | `24h` |
| `SIGNATURE_VALIDITY` | Signed URL validity | `24h` |
| `RTMP_PUBLIC_HOST` | Public hostname for RTMP URLs | `localhost` |

## Usage Guide

### Creating a Stream

1. Log in to Admin Panel: http://localhost:3000/admin
2. Click "New Stream"
3. Fill in details:
   - **Title**: Display name for the stream
   - **Slug**: URL-friendly identifier (auto-generated)
   - **Price**: Cost in EUR
   - **Status**: scheduled/live/ended
4. Save the stream

### Setting Up OBS

After creating a stream, the admin panel shows streaming configuration:

1. Click "Start Container" to launch the Owncast instance
2. Wait for container status to show "running"
3. In OBS, go to Settings → Stream
4. Set Service to "Custom"
5. Copy the **RTMP URL** from admin panel to "Server" field
6. Copy the **Stream Key** to "Stream Key" field
7. Start streaming in OBS
8. Set stream status to "Live" in admin panel

### Granting Free Access (Whitelist)

1. Go to Admin → Edit Stream
2. Scroll to "Email Whitelist" section
3. Add email addresses with optional notes (e.g., "Press", "VIP")
4. Whitelisted users can access via "Already paid?" → enter email

### Viewer Access Flow

1. User visits stream page: `/stream/{slug}`
2. Clicks "Purchase Access"
3. Completes payment via Paytrail
4. Redirected to watch page with access token
5. Token valid for 24 hours

### Token Recovery

If a user loses their session:
1. Go to stream page
2. Click "Already paid?"
3. Enter purchase email
4. Access is restored (works for both paid and whitelisted users)

## API Reference

### Public Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/streams` | List available streams |
| GET | `/api/streams/{slug}` | Get stream details |
| POST | `/api/payment/create` | Initiate payment |
| POST | `/api/payment/recover` | Recover access token |
| GET | `/api/callback/success` | Paytrail success callback |
| POST | `/api/stream/{id}/heartbeat` | Session heartbeat |

### Admin API Endpoints

All admin API endpoints require `X-Admin-Key` header.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/admin/streams` | List all streams |
| POST | `/api/admin/streams` | Create stream |
| GET | `/api/admin/streams/{id}` | Get stream details |
| PUT | `/api/admin/streams/{id}` | Update stream |
| PATCH | `/api/admin/streams/{id}/status` | Update status |
| DELETE | `/api/admin/streams/{id}` | Delete stream |
| GET | `/api/admin/streams/{id}/viewers` | Get viewer count |
| GET | `/api/admin/streams/{id}/payments` | List payments |
| GET | `/api/admin/streams/{id}/whitelist` | List whitelisted emails |
| POST | `/api/admin/streams/{id}/whitelist` | Add to whitelist |
| DELETE | `/api/admin/streams/{id}/whitelist/{email}` | Remove from whitelist |
| GET | `/api/admin/stats` | Get overall stats |

### Admin Web UI Routes

| Path | Description |
|------|-------------|
| `/admin` | Dashboard |
| `/admin/login` | Login page |
| `/admin/streams` | Stream list |
| `/admin/streams/new` | Create stream |
| `/admin/streams/{id}/edit` | Edit stream & whitelist |
| `/admin/streams/{id}/payments` | View payments |

## Database

### Initialization

For fresh installations:
```bash
docker compose exec -T postgres psql -U paywall -d paywall < migrations/init.sql
```

### Migrations

Individual migration files are available for upgrades:
- `001_initial.sql` - Base schema
- `002_dynamic_owncast.sql` - Container management fields
- `003_whitelist.sql` - Email whitelist table

### Tables

- **streams**: Stream configurations and container info
- **payments**: Payment records and access tokens
- **admin_users**: Admin dashboard users
- **stream_whitelist**: Free access email lists

## Security

### Stream Protection

1. **Signed URLs**: Every HLS segment URL is cryptographically signed
2. **Token Validation**: Every request validates access token
3. **Session Tracking**: 30-second heartbeats track active viewers

### Token Recovery

Users who lose their session can recover access by:
1. Clicking "Already paid?" on the stream page
2. Entering their purchase email
3. System validates and issues new token

Rate limited to 5 requests/email/hour and 20 requests/IP/hour.

### Admin Security

- Session-based authentication with bcrypt passwords
- Rate limiting on login attempts
- Separate API key for programmatic access

## Docker Services

The `docker-compose.yml` includes:

- **paywall**: Main Go application
- **postgres**: PostgreSQL database
- **redis**: Session and rate limit storage
- **owncast-***: Dynamically created per-stream containers

## Troubleshooting

### Stream not showing

1. Check container status in admin panel (should be "running")
2. Verify OBS is streaming (check OBS stats)
3. Check paywall logs: `docker compose logs paywall`
4. Verify stream status is "live"

### Payment callback failing

1. Ensure `BASE_URL` is publicly accessible (for Paytrail callbacks)
2. Check Paytrail test mode credentials
3. Review paywall logs for signature errors

### Container won't start

1. Check Docker socket access: `docker ps`
2. Verify port isn't in use: `lsof -i :19350`
3. Check paywall logs for Docker errors

## License

MIT License - see LICENSE file for details.

## References

- [Paytrail API Documentation](https://docs.paytrail.com/)
- [Owncast Documentation](https://owncast.online/docs/)
- [HLS Specification](https://datatracker.ietf.org/doc/html/rfc8216)
- [hls.js Library](https://github.com/video-dev/hls.js/)
