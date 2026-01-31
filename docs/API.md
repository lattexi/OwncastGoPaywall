# API Reference

Complete API documentation for the Stream Paywall server.

## Base URL

- Development: `http://localhost:3000`
- Production: `https://stream.yourdomain.com`

## Authentication

### Public Endpoints
No authentication required.

### Protected Endpoints (Watch/Stream)
Requires valid `access_token` cookie.

### Admin Endpoints
Requires `X-Admin-Key` header.

## Public API

### List Streams

```http
GET /api/streams
```

**Response:**
```json
[
  {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "slug": "my-stream",
    "title": "My Awesome Stream",
    "description": "Stream description",
    "price_cents": 990,
    "start_time": "2024-01-15T18:00:00Z",
    "end_time": "2024-01-15T21:00:00Z",
    "status": "scheduled",
    "max_viewers": 0,
    "created_at": "2024-01-10T12:00:00Z"
  }
]
```

### Get Stream Details

```http
GET /api/streams/{slug}
```

**Response:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "slug": "my-stream",
  "title": "My Awesome Stream",
  "description": "Stream description",
  "price_cents": 990,
  "status": "live",
  "created_at": "2024-01-10T12:00:00Z"
}
```

### Create Payment

```http
POST /api/payment/create
Content-Type: application/json
```

**Request:**
```json
{
  "stream_slug": "my-stream",
  "email": "user@example.com"
}
```

**Response:**
```json
{
  "redirect_url": "https://services.paytrail.com/pay/...",
  "transaction_id": "abc123",
  "payment_id": "550e8400-e29b-41d4-a716-446655440001"
}
```

### Recover Token

```http
POST /api/payment/recover
Content-Type: application/json
```

**Request:**
```json
{
  "stream_slug": "my-stream",
  "email": "user@example.com"
}
```

**Response (Success):**
```json
{
  "success": true,
  "message": "Access recovered successfully",
  "redirect_url": "/watch/my-stream"
}
```

**Response (Not Found):**
```json
{
  "error": "No active purchase found for this email."
}
```

**Response (Rate Limited):**
```json
{
  "error": "Too many recovery attempts. Please try again later."
}
```

### Heartbeat

```http
POST /api/stream/{id}/heartbeat
X-Device-ID: device-fingerprint-hash
```

**Response:**
```json
{
  "success": true,
  "message": "Heartbeat received"
}
```

### Get Playlist URL

```http
GET /api/stream/{slug}/playlist
Cookie: access_token=...
```

**Response:**
```json
{
  "playlist_url": "http://localhost:3000/stream/.../hls/stream.m3u8?token=...&expires=...&sig=..."
}
```

## Admin API

All admin endpoints require:
```http
X-Admin-Key: your-admin-api-key
```

### List All Streams

```http
GET /admin/streams
```

**Response:**
```json
[
  {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "slug": "my-stream",
    "title": "My Stream",
    "owncast_url": "http://owncast:8080",
    "status": "live",
    ...
  }
]
```

### Create Stream

```http
POST /admin/streams
Content-Type: application/json
```

**Request:**
```json
{
  "slug": "new-stream",
  "title": "New Stream",
  "description": "Optional description",
  "price_cents": 990,
  "owncast_url": "http://owncast:8080",
  "start_time": "2024-01-15T18:00:00Z",
  "end_time": "2024-01-15T21:00:00Z",
  "max_viewers": 100
}
```

**Response:** Created stream object (201)

### Get Stream

```http
GET /admin/streams/{id}
```

### Update Stream

```http
PUT /admin/streams/{id}
Content-Type: application/json
```

**Request:** (all fields optional)
```json
{
  "title": "Updated Title",
  "description": "Updated description",
  "price_cents": 1490,
  "status": "live",
  "owncast_url": "http://new-owncast:8080"
}
```

### Update Stream Status

```http
PATCH /admin/streams/{id}/status
Content-Type: application/json
```

**Request:**
```json
{
  "status": "live"
}
```

Valid statuses: `scheduled`, `live`, `ended`

### Delete Stream

```http
DELETE /admin/streams/{id}
```

**Response:**
```json
{
  "success": true,
  "message": "Stream deleted"
}
```

### Get Viewer Count

```http
GET /admin/streams/{id}/viewers
```

**Response:**
```json
{
  "stream_id": "550e8400-e29b-41d4-a716-446655440000",
  "viewer_count": 42
}
```

### List Payments

```http
GET /admin/streams/{id}/payments
```

**Response:**
```json
[
  {
    "id": "...",
    "stream_id": "...",
    "email": "user@example.com",
    "amount_cents": 990,
    "status": "completed",
    "paytrail_ref": "...",
    "paytrail_transaction_id": "...",
    "token_preview": "abc12345...",
    "token_expiry": "2024-01-16T18:00:00Z",
    "created_at": "2024-01-15T10:00:00Z"
  }
]
```

### Get Stats

```http
GET /admin/stats
```

**Response:**
```json
{
  "total_streams": 5,
  "total_payments": 150,
  "completed_payments": 142,
  "total_revenue_cents": 140580,
  "total_revenue_euros": 1405.80,
  "active_viewers": 23
}
```

## Error Responses

All endpoints may return these error formats:

**400 Bad Request:**
```json
{
  "error": "Invalid request body"
}
```

**401 Unauthorized:**
```json
{
  "error": "Unauthorized"
}
```

**403 Forbidden:**
```json
{
  "error": "Invalid signature"
}
```

**404 Not Found:**
```json
{
  "error": "Stream not found"
}
```

**429 Too Many Requests:**
```json
{
  "error": "Device limit exceeded. Stream is playing on another device."
}
```

**500 Internal Server Error:**
```json
{
  "error": "Internal server error"
}
```

## Rate Limits

| Endpoint | Limit |
|----------|-------|
| `/api/payment/recover` | 5/email/hour, 20/IP/hour |
| `/api/payment/create` | 10/IP/minute |
| `/admin/*` | No limit (protected by API key) |

## Webhooks

Paytrail callbacks are received at:
- Success: `GET /api/callback/success`
- Cancel: `GET /api/callback/cancel`

Both include signature verification.
