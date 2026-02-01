package paytrail

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// CalculateSignature calculates the HMAC-SHA256 signature for Paytrail API
// Headers must be the checkout-* headers, body is the request body (empty string for GET)
func CalculateSignature(secret string, headers map[string]string, body string) string {
	// Get all checkout-* header keys and sort them
	var keys []string
	for k := range headers {
		if strings.HasPrefix(strings.ToLower(k), "checkout-") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	// Build the signature payload
	var parts []string
	for _, k := range keys {
		parts = append(parts, strings.ToLower(k)+":"+headers[k])
	}

	// Join with newlines and append body
	payload := strings.Join(parts, "\n")
	if body != "" {
		payload += "\n" + body
	}

	// Calculate HMAC-SHA256
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))

	return hex.EncodeToString(h.Sum(nil))
}

// VerifySignature verifies a Paytrail callback signature
// Uses constant-time comparison to prevent timing attacks
func VerifySignature(secret, signature string, params map[string]string) bool {
	// Get all checkout-* parameter keys and sort them
	var keys []string
	for k := range params {
		if strings.HasPrefix(strings.ToLower(k), "checkout-") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	// Build the signature payload (same as headers but from query params)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+":"+params[k])
	}

	// Join with newlines (no body for callbacks)
	payload := strings.Join(parts, "\n")

	// Calculate expected signature
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))
	expected := hex.EncodeToString(h.Sum(nil))

	log.Debug().
		Str("payload", payload).
		Str("expected", expected).
		Str("received", signature).
		Bool("match", subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1).
		Msg("Verifying Paytrail signature")

	// Constant-time comparison
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

// VerifyCallbackSignature verifies a signature from URL query parameters
func VerifyCallbackSignature(secret string, queryParams url.Values) bool {
	// Extract signature
	signature := queryParams.Get("signature")
	if signature == "" {
		return false
	}

	// Convert query params to map (excluding signature itself)
	params := make(map[string]string)
	for k, v := range queryParams {
		if k != "signature" && len(v) > 0 {
			params[k] = v[0]
		}
	}

	return VerifySignature(secret, signature, params)
}

// ExtractCallbackParams extracts Paytrail callback parameters from query string
func ExtractCallbackParams(queryParams url.Values) *CallbackParams {
	return &CallbackParams{
		Account:       queryParams.Get("checkout-account"),
		Algorithm:     queryParams.Get("checkout-algorithm"),
		Amount:        queryParams.Get("checkout-amount"),
		Stamp:         queryParams.Get("checkout-stamp"),
		Reference:     queryParams.Get("checkout-reference"),
		TransactionID: queryParams.Get("checkout-transaction-id"),
		Status:        queryParams.Get("checkout-status"),
		Provider:      queryParams.Get("checkout-provider"),
	}
}

// CallbackParams represents the parameters received in a Paytrail callback
type CallbackParams struct {
	Account       string
	Algorithm     string
	Amount        string
	Stamp         string
	Reference     string
	TransactionID string
	Status        string
	Provider      string
}

// IsSuccessful returns true if the payment was successful
func (p *CallbackParams) IsSuccessful() bool {
	return p.Status == "ok"
}

// IsPending returns true if the payment is pending
func (p *CallbackParams) IsPending() bool {
	return p.Status == "pending"
}

// IsFailed returns true if the payment failed
func (p *CallbackParams) IsFailed() bool {
	return p.Status == "fail"
}
