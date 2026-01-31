# Paytrail Integration Guide

This guide covers integrating with Paytrail, a Finnish payment provider.

## Overview

Paytrail is used for processing payments for stream access. The integration includes:
- Creating payments with HMAC signature
- Handling success/cancel callbacks
- Verifying callback signatures

## Getting Started

### 1. Create Paytrail Account

1. Sign up at [Paytrail Merchant Panel](https://merchant.paytrail.com/)
2. Complete merchant verification
3. Note your **Merchant ID** and **Secret Key**

### 2. Test Credentials

For development, use Paytrail's test credentials:

```
Merchant ID: 375917
Secret Key: SAIPPUAKAUPPIAS
```

Test cards and payment methods are documented at [Paytrail Test Credentials](https://docs.paytrail.com/#/?id=test-credentials).

### 3. Configure Environment

```bash
# .env
PAYTRAIL_MERCHANT_ID=375917
PAYTRAIL_SECRET_KEY=SAIPPUAKAUPPIAS
```

## Payment Flow

```
1. User clicks "Purchase" on stream page
2. Frontend calls POST /api/payment/create
3. Server creates Paytrail payment
4. User is redirected to Paytrail checkout
5. User completes payment
6. Paytrail redirects to /api/callback/success
7. Server verifies signature and creates access token
8. User is redirected to watch page
```

### Sequence Diagram

```
User        Frontend       Paywall Server      Paytrail
 │             │                │                 │
 │  Purchase   │                │                 │
 │────────────>│                │                 │
 │             │ POST /api/     │                 │
 │             │ payment/create │                 │
 │             │───────────────>│                 │
 │             │                │ POST /payments  │
 │             │                │────────────────>│
 │             │                │  redirect_url   │
 │             │                │<────────────────│
 │             │  redirect_url  │                 │
 │             │<───────────────│                 │
 │  Redirect to Paytrail       │                 │
 │─────────────────────────────────────────────>│
 │                             │                 │
 │          Payment Form       │                 │
 │<─────────────────────────────────────────────│
 │                             │                 │
 │     Complete Payment        │                 │
 │─────────────────────────────────────────────>│
 │                             │                 │
 │         Redirect to callback                  │
 │<─────────────────────────────────────────────│
 │             │                │                 │
 │ GET /api/callback/success   │                 │
 │────────────────────────────>│                 │
 │             │                │ Verify sig      │
 │             │                │ Create token    │
 │ Redirect to /watch/{slug}   │                 │
 │<────────────────────────────│                 │
```

## Signature Calculation

All Paytrail API requests must be signed with HMAC-SHA256.

### Request Signature

```go
// Collect checkout-* headers
headers := map[string]string{
    "checkout-account":   merchantID,
    "checkout-algorithm": "sha256",
    "checkout-method":    "POST",
    "checkout-nonce":     uuid.New().String(),
    "checkout-timestamp": time.Now().UTC().Format(time.RFC3339),
}

// Sort headers alphabetically
// Join as "key:value" with newlines
// Append request body
// Calculate HMAC-SHA256
// Encode as lowercase hex
```

### Callback Signature Verification

Paytrail callbacks include a `signature` query parameter:

```go
// Extract all checkout-* query params
// Sort alphabetically
// Join as "key:value" with newlines
// Calculate HMAC-SHA256
// Compare with provided signature (constant-time!)
```

## Callback Parameters

Success callback includes these parameters:

| Parameter | Description |
|-----------|-------------|
| `checkout-account` | Merchant ID |
| `checkout-algorithm` | Signature algorithm |
| `checkout-amount` | Payment amount (cents) |
| `checkout-stamp` | Our payment reference |
| `checkout-reference` | Order reference |
| `checkout-transaction-id` | Paytrail transaction ID |
| `checkout-status` | `ok`, `pending`, or `fail` |
| `checkout-provider` | Payment method used |
| `signature` | HMAC signature |

## Error Handling

### Payment Creation Errors

```go
// Handle Paytrail API errors
if resp.StatusCode != 200 && resp.StatusCode != 201 {
    // Log error, return user-friendly message
    return fmt.Errorf("payment failed: %s", resp.Status)
}
```

### Callback Errors

```go
// Invalid signature
if !paytrail.VerifyCallbackSignature(secret, queryParams) {
    http.Error(w, "Invalid signature", http.StatusForbidden)
    return
}

// Payment not found
if payment == nil {
    http.Error(w, "Payment not found", http.StatusNotFound)
    return
}
```

## Testing

### Test Cards

| Card Number | Result |
|-------------|--------|
| 4153 0139 9970 0024 | Success |
| 4153 0139 9970 0313 | Declined |

### Test Flow

1. Create a test stream
2. Start purchase flow
3. Use test card at Paytrail
4. Verify callback is received
5. Verify access token is created

```bash
# Create test payment
curl -X POST http://localhost:3000/api/payment/create \
  -H "Content-Type: application/json" \
  -d '{"stream_slug":"test","email":"test@example.com"}'
```

## Production Checklist

- [ ] Replace test credentials with production credentials
- [ ] Verify `BASE_URL` is correct for callbacks
- [ ] Test full payment flow with real payment method
- [ ] Monitor callback delivery in Paytrail dashboard
- [ ] Set up webhook for failed callback retries

## Webhook Setup (Optional)

Configure webhooks in Paytrail merchant panel for:
- Payment success notifications
- Refund notifications
- Failed payment notifications

## Refunds

Refunds can be processed via Paytrail API:

```bash
POST /payments/{transactionId}/refund
```

Not currently implemented in the paywall, but can be added.

## References

- [Paytrail API Documentation](https://docs.paytrail.com/)
- [Paytrail OpenAPI Spec](https://docs.paytrail.com/paytrail-api.yaml)
- [Test Credentials](https://docs.paytrail.com/#/?id=test-credentials)
- [Payment Method Providers](https://docs.paytrail.com/#/payment-method-providers)
