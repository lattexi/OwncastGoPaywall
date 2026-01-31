# Stream Paywall

A production-ready Go server that acts as a paywall proxy for Owncast video streams, integrating Paytrail payments (Finnish payment provider) with multi-layer anti-sharing protection.

## Features

- **Paytrail Integration**: Finnish payment provider support with HMAC signature verification
- **HLS Stream Proxying**: Proxies Owncast streams while hiding the backend URL
- **Signed URLs**: Cryptographically signed, time-limited segment URLs
- **Single Device Enforcement**: Only one device can watch per purchase at a time
- **Device Fingerprinting**: Client-side fingerprint generation for device tracking
- **Token Recovery**: Users can recover their access using their purchase email
- **Admin API**: Full CRUD for streams, viewer stats, payment management

## Architecture

```
Internet                              Internal Network
                                      
┌──────────┐    HTTPS    ┌───────────────────┐    HTTP     ┌──────────┐
│  Client  │────────────▶│   Go Server       │────────────▶│ Owncast  │
│(browser) │◀────────────│   (paywall)       │◀────────────│(no public│
└──────────┘             │                   │             │ access)  │
                         │ • Paytrail payments│            └──────────┘
                         │ • Authentication   │
                         │ • Device limiting  │
                         │ • HLS proxying     │
                         └───────────────────┘
```

**Critical Security**: The client never sees or can discover the Owncast server's real URL. All stream requests go through the Go proxy.

## Quick Start

### Prerequisites

- Go 1.22+
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

3. Start dependencies with Docker:
   ```bash
   docker compose up -d postgres redis
   ```

4. Run migrations:
   ```bash
   make migrate
   ```

5. Run the server:
   ```bash
   make dev
   ```

### Production Deployment

```bash
# Configure environment
cp .env.example .env
# Edit .env with production values

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
| `DATABASE_URL` | PostgreSQL connection string | - |
| `REDIS_URL` | Redis connection string | - |
| `SESSION_DURATION` | Access token validity | `24h` |
| `HEARTBEAT_TIMEOUT` | Device timeout | `45s` |
| `SIGNATURE_VALIDITY` | Signed URL validity | `30s` |

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

### Admin Endpoints

All admin endpoints require `X-Admin-Key` header.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/admin/streams` | List all streams |
| POST | `/admin/streams` | Create stream |
| GET | `/admin/streams/{id}` | Get stream details |
| PUT | `/admin/streams/{id}` | Update stream |
| PATCH | `/admin/streams/{id}/status` | Update status |
| DELETE | `/admin/streams/{id}` | Delete stream |
| GET | `/admin/streams/{id}/viewers` | Get viewer count |
| GET | `/admin/streams/{id}/payments` | List payments |
| GET | `/admin/stats` | Get overall stats |

### Creating a Stream

```bash
curl -X POST http://localhost:3000/admin/streams \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: your-admin-key" \
  -d '{
    "slug": "my-stream",
    "title": "My Live Stream",
    "description": "An awesome stream",
    "price_cents": 990,
    "owncast_url": "http://owncast:8080"
  }'
```

### Setting Stream Live

```bash
curl -X PATCH http://localhost:3000/admin/streams/{id}/status \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: your-admin-key" \
  -d '{"status": "live"}'
```

## Documentation

- [Deployment Guide](docs/DEPLOYMENT.md)
- [Owncast Setup](docs/OWNCAST_SETUP.md)
- [Paytrail Integration](docs/PAYTRAIL_INTEGRATION.md)
- [API Reference](docs/API.md)
- [Security Architecture](docs/SECURITY.md)

## Testing

```bash
# Run all tests
make test

# Run with coverage
make test-coverage

# Run linter
make lint
```

## Security

### Anti-Sharing Protection

1. **Signed URLs**: Every HLS segment URL is cryptographically signed with a 30-second expiry
2. **Device Fingerprinting**: Browser fingerprint tracks the viewing device
3. **Single Device Enforcement**: Only one device can watch per token at a time
4. **Heartbeat Mechanism**: 30-second heartbeats with 45-second timeout

### Token Recovery

Users who lose their session can recover access by:
1. Clicking "I already paid" on the stream page
2. Entering their purchase email
3. System validates and issues new token (old one invalidated)

Rate limited to 5 requests/email/hour and 20 requests/IP/hour.

## License

MIT License - see LICENSE file for details.

## References

- [Paytrail API Documentation](https://docs.paytrail.com/)
- [Owncast Documentation](https://owncast.online/docs/)
- [HLS Specification](https://datatracker.ietf.org/doc/html/rfc8216)
- [hls.js Library](https://github.com/video-dev/hls.js/)
