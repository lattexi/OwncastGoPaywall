package paytrail

import (
	"testing"
)

func TestCalculateSignature(t *testing.T) {
	// Test case based on Paytrail documentation example
	secret := "SAIPPUAKAUPPIAS"
	
	headers := map[string]string{
		"checkout-account":   "375917",
		"checkout-algorithm": "sha256",
		"checkout-method":    "POST",
		"checkout-nonce":     "564635208570151",
		"checkout-timestamp": "2018-07-06T10:01:31.904Z",
	}

	body := `{"stamp":"unique-identifier-for-merchant","reference":"3759170","amount":1525,"currency":"EUR","language":"FI","items":[{"unitPrice":1525,"units":1,"vatPercentage":24,"productCode":"#1234","deliveryDate":"2018-09-01"}],"customer":{"email":"test@test.com"},"redirectUrls":{"success":"https://ecom.example.com/cart/success","cancel":"https://ecom.example.com/cart/cancel"}}`

	signature := CalculateSignature(secret, headers, body)

	// The expected signature should match what Paytrail would generate
	// Since we don't have the exact expected value from docs, we verify the format
	if len(signature) != 64 {
		t.Errorf("Expected signature length 64, got %d", len(signature))
	}

	// Verify signature is hex encoded
	for _, c := range signature {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("Signature contains non-hex character: %c", c)
		}
	}
}

func TestVerifySignature(t *testing.T) {
	secret := "SAIPPUAKAUPPIAS"

	// Create test params
	params := map[string]string{
		"checkout-account":        "375917",
		"checkout-algorithm":      "sha256",
		"checkout-amount":         "2964",
		"checkout-stamp":          "15336332710015",
		"checkout-reference":      "192387192837195",
		"checkout-transaction-id": "4b300af6-9a22-11e8-9184-abb6de7fd2d0",
		"checkout-status":         "ok",
		"checkout-provider":       "nordea",
	}

	// Calculate the correct signature
	correctSignature := CalculateSignature(secret, params, "")

	// Should verify correctly
	if !VerifySignature(secret, correctSignature, params) {
		t.Error("Expected signature verification to succeed")
	}

	// Should fail with wrong signature
	if VerifySignature(secret, "wrong-signature", params) {
		t.Error("Expected signature verification to fail with wrong signature")
	}

	// Should fail with tampered params
	tamperedParams := make(map[string]string)
	for k, v := range params {
		tamperedParams[k] = v
	}
	tamperedParams["checkout-amount"] = "9999"
	if VerifySignature(secret, correctSignature, tamperedParams) {
		t.Error("Expected signature verification to fail with tampered params")
	}
}

func TestCallbackParamsStatus(t *testing.T) {
	tests := []struct {
		status       string
		isSuccessful bool
		isPending    bool
		isFailed     bool
	}{
		{"ok", true, false, false},
		{"pending", false, true, false},
		{"fail", false, false, true},
		{"delayed", false, false, false},
	}

	for _, tt := range tests {
		params := &CallbackParams{Status: tt.status}
		if params.IsSuccessful() != tt.isSuccessful {
			t.Errorf("Status %s: IsSuccessful() = %v, want %v", tt.status, params.IsSuccessful(), tt.isSuccessful)
		}
		if params.IsPending() != tt.isPending {
			t.Errorf("Status %s: IsPending() = %v, want %v", tt.status, params.IsPending(), tt.isPending)
		}
		if params.IsFailed() != tt.isFailed {
			t.Errorf("Status %s: IsFailed() = %v, want %v", tt.status, params.IsFailed(), tt.isFailed)
		}
	}
}
