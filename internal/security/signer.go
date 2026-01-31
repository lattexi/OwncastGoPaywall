package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// URLSigner handles signing and verification of stream URLs
type URLSigner struct {
	secret   string
	validity time.Duration
}

// NewURLSigner creates a new URL signer
func NewURLSigner(secret string, validity time.Duration) *URLSigner {
	return &URLSigner{
		secret:   secret,
		validity: validity,
	}
}

// SignURL generates a signed URL for a stream segment
// Input format: {streamID}:{token}:{path}:{expires}
func (s *URLSigner) SignURL(streamID, token, path string) string {
	expires := time.Now().Add(s.validity).Unix()
	
	// Build signature input
	input := fmt.Sprintf("%s:%s:%s:%d", streamID, token, path, expires)
	
	// Calculate HMAC-SHA256
	h := hmac.New(sha256.New, []byte(s.secret))
	h.Write([]byte(input))
	sig := hex.EncodeToString(h.Sum(nil))
	
	// Build query string
	params := url.Values{}
	params.Set("token", token)
	params.Set("expires", strconv.FormatInt(expires, 10))
	params.Set("sig", sig)
	
	return path + "?" + params.Encode()
}

// SignedURLParams contains the parameters needed to verify a signed URL
type SignedURLParams struct {
	Token   string
	Expires int64
	Sig     string
}

// ParseSignedURL extracts signing parameters from a URL
func ParseSignedURL(u *url.URL) (*SignedURLParams, error) {
	token := u.Query().Get("token")
	expiresStr := u.Query().Get("expires")
	sig := u.Query().Get("sig")
	
	if token == "" || expiresStr == "" || sig == "" {
		return nil, fmt.Errorf("missing required parameters")
	}
	
	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid expires value")
	}
	
	return &SignedURLParams{
		Token:   token,
		Expires: expires,
		Sig:     sig,
	}, nil
}

// VerifyURL verifies a signed URL
// Returns nil if valid, error if invalid
func (s *URLSigner) VerifyURL(streamID, path string, params *SignedURLParams) error {
	// Check expiry first
	if time.Now().Unix() > params.Expires {
		return fmt.Errorf("signature expired")
	}
	
	// Rebuild signature input
	input := fmt.Sprintf("%s:%s:%s:%d", streamID, params.Token, path, params.Expires)
	
	// Calculate expected signature
	h := hmac.New(sha256.New, []byte(s.secret))
	h.Write([]byte(input))
	expected := hex.EncodeToString(h.Sum(nil))
	
	// Constant-time comparison
	if subtle.ConstantTimeCompare([]byte(expected), []byte(params.Sig)) != 1 {
		return fmt.Errorf("invalid signature")
	}
	
	return nil
}

// VerifyURLFromRequest is a convenience method that parses and verifies a URL
func (s *URLSigner) VerifyURLFromRequest(streamID, path string, queryParams url.Values) error {
	params := &SignedURLParams{
		Token:   queryParams.Get("token"),
		Sig:     queryParams.Get("sig"),
	}
	
	expiresStr := queryParams.Get("expires")
	if expiresStr == "" {
		return fmt.Errorf("missing expires parameter")
	}
	
	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expires value")
	}
	params.Expires = expires
	
	return s.VerifyURL(streamID, path, params)
}
