# Security Architecture

This document describes the security measures implemented in the Stream Paywall.

## Overview

The system implements multiple layers of security to:
1. Protect streams from unauthorized access
2. Prevent link/token sharing
3. Secure payment processing
4. Protect user data

## Stream Protection

### Network Isolation

Owncast runs on an internal Docker network with no public port exposure:

```yaml
networks:
  internal:
    internal: true  # No external access
```

The only way to access streams is through the paywall proxy.

### URL Signing

Every HLS segment URL is cryptographically signed:

```
/stream/{streamID}/hls/segment.ts?token={accessToken}&expires={unix}&sig={hmac}
```

**Signature Components:**
- Stream ID
- Access token
- Path
- Expiry timestamp

**Signature Algorithm:** HMAC-SHA256 with configurable secret

**Expiry:** 30 seconds by default (configurable)

```go
// Signature calculation
input := fmt.Sprintf("%s:%s:%s:%d", streamID, token, path, expires)
sig := hmac.New(sha256.New, []byte(secret))
sig.Write([]byte(input))
return hex.EncodeToString(sig.Sum(nil))
```

### Manifest Rewriting

HLS manifests (`.m3u8`) are fetched from Owncast and rewritten:

1. All segment URLs replaced with proxy URLs
2. Each URL signed with current token
3. Original Owncast URLs never exposed

## Anti-Sharing Protection

### Layer 1: Signed URLs (30s expiry)

Even if a URL is shared, it expires in 30 seconds.

### Layer 2: Device Fingerprinting

Client-side fingerprint combines:
- User agent
- Screen resolution
- Timezone
- Canvas fingerprint
- WebGL renderer
- Audio context

Hashed with SHA-256 and sent via `X-Device-ID` header.

### Layer 3: Single Device Enforcement

Only one device can use an access token at a time:

```
Request with Device A → Allowed, Device A registered
Request with Device A → Allowed, LastSeen updated
Request with Device B → Check Device A LastSeen
  - If LastSeen > 45s ago → Allowed, Device B registered
  - If LastSeen < 45s ago → HTTP 429 Too Many Requests
```

**Implementation:**
```go
if currentDevice.DeviceID != requestDevice {
    if time.Since(currentDevice.LastSeen) < heartbeatTimeout {
        return http.StatusTooManyRequests
    }
    // Allow device switch
}
```

### Heartbeat Mechanism

- Player sends heartbeat every 30 seconds
- Updates `LastSeen` timestamp in Redis
- 45-second timeout before device switch allowed

## Payment Security

### Paytrail Integration

All Paytrail API calls are signed with HMAC-SHA256:

```go
// Headers included in signature
checkout-account
checkout-algorithm
checkout-method
checkout-nonce
checkout-timestamp
```

### Callback Verification

Paytrail callbacks are verified using constant-time comparison:

```go
// Prevents timing attacks
subtle.ConstantTimeCompare([]byte(expected), []byte(received))
```

### Access Token Generation

- 256-bit random tokens (64 hex characters)
- Generated using `crypto/rand`
- Stored hashed in database (future enhancement)

## Token Recovery

### Rate Limiting

- 5 requests per email per hour
- 20 requests per IP per hour
- Redis-based counters with TTL

### Timing Attack Prevention

All recovery requests take the same time regardless of whether the email exists:

```go
startTime := time.Now()
defer func() {
    elapsed := time.Since(startTime)
    if elapsed < 500*time.Millisecond {
        time.Sleep(500*time.Millisecond - elapsed)
    }
}()
```

### Email Enumeration Prevention

- Generic error message: "No active purchase found for this email"
- No distinction between "email not found" and "no purchase"
- Email addresses hashed in rate limit keys

## API Security

### Admin Authentication

Admin endpoints require API key in header:

```http
X-Admin-Key: your-secret-key
```

Verified using constant-time comparison.

### Input Validation

All inputs are validated:
- Email format validation
- UUID format validation
- Enum validation for status fields
- Price must be non-negative

### SQL Injection Prevention

Using parameterized queries (pgx):

```go
query := "SELECT * FROM streams WHERE slug = $1"
rows, err := pool.Query(ctx, query, slug)
```

## Data Protection

### Sensitive Data Handling

| Data | Storage | Exposure |
|------|---------|----------|
| Owncast URL | PostgreSQL | Never (json:"-") |
| Access Token | PostgreSQL/Redis | Cookie only, HttpOnly |
| Email | PostgreSQL | Admin API only |
| Device Fingerprint | Redis | Never exposed |

### Cookie Security

```go
http.Cookie{
    Name:     "access_token",
    HttpOnly: true,        // No JavaScript access
    Secure:   true,        // HTTPS only (production)
    SameSite: http.SameSiteLaxMode,
}
```

## Secrets Management

### Required Secrets

| Secret | Purpose | Minimum Length |
|--------|---------|----------------|
| `SIGNING_SECRET` | URL signing | 32 characters |
| `ADMIN_API_KEY` | Admin auth | 32 characters |
| `PAYTRAIL_SECRET_KEY` | Payment signing | From Paytrail |
| `POSTGRES_PASSWORD` | Database auth | 16 characters |

### Best Practices

1. Generate secrets with cryptographically secure random generator:
   ```bash
   openssl rand -hex 32
   ```

2. Never commit secrets to version control

3. Use environment variables or secret management system

4. Rotate secrets periodically

## Logging

### Security Events Logged

- Failed signature verifications
- Rate limit exceeded
- Device limit exceeded
- Payment callback received
- Token recovery attempts
- Admin API access

### Sensitive Data Excluded

- Full access tokens (truncated to 8 chars)
- Full email addresses in debug logs
- Owncast URLs

## Recommendations

### Production Checklist

- [ ] Use HTTPS only
- [ ] Set strong secrets (32+ characters)
- [ ] Enable firewall (only 80/443 exposed)
- [ ] Configure rate limiting on reverse proxy
- [ ] Enable security headers (CSP, HSTS, etc.)
- [ ] Regular security updates
- [ ] Monitor logs for suspicious activity
- [ ] Regular backup encryption

### Future Enhancements

1. Hash access tokens in database (bcrypt)
2. Implement refresh tokens
3. Add IP-based rate limiting to all endpoints
4. Implement CAPTCHA for purchase flow
5. Add two-factor authentication for admin
6. Implement Content Security Policy headers
