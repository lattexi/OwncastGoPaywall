package security

import (
	"testing"
	"time"
)

func TestURLSigner(t *testing.T) {
	secret := "test-secret-key"
	validity := 30 * time.Second
	signer := NewURLSigner(secret, validity)

	streamID := "stream-123"
	token := "access-token-abc"
	path := "/stream/stream-123/hls/segment0.ts"

	// Sign URL
	signedPath := signer.SignURL(streamID, token, path)
	t.Logf("Signed path: %s", signedPath)

	// Parse the signed URL
	// The signedPath includes the original path + query params
	// We need to extract query params for verification
	
	// Parse manually
	qIndex := 0
	for i, c := range signedPath {
		if c == '?' {
			qIndex = i
			break
		}
	}
	
	if qIndex == 0 {
		t.Fatal("No query params found in signed URL")
	}

	// Extract query string
	queryStr := signedPath[qIndex+1:]
	
	// Parse query params manually for this test
	params := parseQueryString(queryStr)
	
	signedParams := &SignedURLParams{
		Token:   params["token"],
		Sig:     params["sig"],
	}
	
	expiresStr := params["expires"]
	if expiresStr == "" {
		t.Fatal("No expires param found")
	}
	
	// Parse expires
	var expires int64
	for _, c := range expiresStr {
		if c >= '0' && c <= '9' {
			expires = expires*10 + int64(c-'0')
		}
	}
	signedParams.Expires = expires

	// Verify should succeed
	err := signer.VerifyURL(streamID, path, signedParams)
	if err != nil {
		t.Errorf("Expected verification to succeed, got error: %v", err)
	}

	// Verify with wrong token should fail
	wrongParams := *signedParams
	wrongParams.Token = "wrong-token"
	err = signer.VerifyURL(streamID, path, &wrongParams)
	if err == nil {
		t.Error("Expected verification to fail with wrong token")
	}

	// Verify with wrong stream ID should fail
	err = signer.VerifyURL("wrong-stream", path, signedParams)
	if err == nil {
		t.Error("Expected verification to fail with wrong stream ID")
	}

	// Verify with wrong path should fail
	err = signer.VerifyURL(streamID, "/wrong/path", signedParams)
	if err == nil {
		t.Error("Expected verification to fail with wrong path")
	}
}

func TestURLSignerExpiry(t *testing.T) {
	secret := "test-secret-key"
	validity := 1 * time.Second // Very short validity
	signer := NewURLSigner(secret, validity)

	streamID := "stream-123"
	token := "access-token-abc"
	path := "/stream/stream-123/hls/segment0.ts"

	// Sign URL
	signedPath := signer.SignURL(streamID, token, path)

	// Parse params
	qIndex := 0
	for i, c := range signedPath {
		if c == '?' {
			qIndex = i
			break
		}
	}
	queryStr := signedPath[qIndex+1:]
	params := parseQueryString(queryStr)

	signedParams := &SignedURLParams{
		Token: params["token"],
		Sig:   params["sig"],
	}
	var expires int64
	for _, c := range params["expires"] {
		if c >= '0' && c <= '9' {
			expires = expires*10 + int64(c-'0')
		}
	}
	signedParams.Expires = expires

	// Should be valid immediately
	err := signer.VerifyURL(streamID, path, signedParams)
	if err != nil {
		t.Errorf("Expected verification to succeed immediately, got: %v", err)
	}

	// Wait for expiry
	time.Sleep(2 * time.Second)

	// Should be expired now
	err = signer.VerifyURL(streamID, path, signedParams)
	if err == nil {
		t.Error("Expected verification to fail after expiry")
	}
}

// Simple query string parser for tests
func parseQueryString(qs string) map[string]string {
	result := make(map[string]string)
	pairs := splitString(qs, '&')
	for _, pair := range pairs {
		parts := splitString(pair, '=')
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

func splitString(s string, sep rune) []string {
	var result []string
	start := 0
	for i, c := range s {
		if c == sep {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}
