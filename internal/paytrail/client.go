package paytrail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

const (
	// PaytrailAPIURL is the Paytrail API endpoint
	PaytrailAPIURL = "https://services.paytrail.com"

	// Algorithm used for HMAC
	Algorithm = "sha256"
)

// Client is a Paytrail API client
type Client struct {
	merchantID string
	secretKey  string
	httpClient *http.Client
}

// NewClient creates a new Paytrail client
func NewClient(merchantID, secretKey string) *Client {
	return &Client{
		merchantID: merchantID,
		secretKey:  secretKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CreatePaymentRequest is the request body for creating a payment
type CreatePaymentRequest struct {
	Stamp        string        `json:"stamp"`
	Reference    string        `json:"reference"`
	Amount       int           `json:"amount"`
	Currency     string        `json:"currency"`
	Language     string        `json:"language"`
	Items        []PaymentItem `json:"items"`
	Customer     Customer      `json:"customer"`
	RedirectURLs RedirectURLs  `json:"redirectUrls"`
	CallbackURLs *CallbackURLs `json:"callbackUrls,omitempty"`
}

// PaymentItem represents an item in the payment
type PaymentItem struct {
	UnitPrice     int    `json:"unitPrice"`
	Units         int    `json:"units"`
	VATPercentage int    `json:"vatPercentage"`
	ProductCode   string `json:"productCode"`
	Description   string `json:"description,omitempty"`
	Stamp         string `json:"stamp,omitempty"`
}

// Customer represents the customer information
type Customer struct {
	Email     string `json:"email"`
	FirstName string `json:"firstName,omitempty"`
	LastName  string `json:"lastName,omitempty"`
	Phone     string `json:"phone,omitempty"`
}

// RedirectURLs contains the URLs for redirecting the customer
type RedirectURLs struct {
	Success string `json:"success"`
	Cancel  string `json:"cancel"`
}

// CallbackURLs contains the URLs for server-to-server callbacks
type CallbackURLs struct {
	Success string `json:"success"`
	Cancel  string `json:"cancel"`
}

// CreatePaymentResponse is the response from creating a payment
type CreatePaymentResponse struct {
	TransactionID string             `json:"transactionId"`
	Href          string             `json:"href"`
	Reference     string             `json:"reference"`
	Terms         string             `json:"terms"`
	Groups        []PaymentGroup     `json:"groups"`
	Providers     []PaymentProvider  `json:"providers"`
}

// PaymentGroup represents a group of payment methods
type PaymentGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
	SVG  string `json:"svg"`
}

// PaymentProvider represents a payment provider
type PaymentProvider struct {
	URL        string       `json:"url"`
	Icon       string       `json:"icon"`
	SVG        string       `json:"svg"`
	Name       string       `json:"name"`
	Group      string       `json:"group"`
	ID         string       `json:"id"`
	Parameters []FormField  `json:"parameters"`
}

// FormField represents a form field for payment submission
type FormField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CreatePayment creates a new payment
func (c *Client) CreatePayment(ctx context.Context, req *CreatePaymentRequest) (*CreatePaymentResponse, error) {
	// Generate nonce and timestamp
	nonce := uuid.New().String()
	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Serialize request body
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build headers for signature
	headers := map[string]string{
		"checkout-account":   c.merchantID,
		"checkout-algorithm": Algorithm,
		"checkout-method":    "POST",
		"checkout-nonce":     nonce,
		"checkout-timestamp": timestamp,
	}

	// Calculate signature
	signature := CalculateSignature(c.secretKey, headers, string(body))

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", PaytrailAPIURL+"/payments", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	httpReq.Header.Set("checkout-account", c.merchantID)
	httpReq.Header.Set("checkout-algorithm", Algorithm)
	httpReq.Header.Set("checkout-method", "POST")
	httpReq.Header.Set("checkout-nonce", nonce)
	httpReq.Header.Set("checkout-timestamp", timestamp)
	httpReq.Header.Set("signature", signature)

	// Send request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check for errors
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("paytrail API error: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var result CreatePaymentResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

// SimplePaymentRequest is a simplified payment request for common use cases
type SimplePaymentRequest struct {
	Stamp       string // Unique identifier for this payment
	Reference   string // Order reference
	Amount      int    // Amount in cents
	Description string // Product description
	Email       string // Customer email
	SuccessURL  string // Redirect URL on success
	CancelURL   string // Redirect URL on cancel
	CallbackURL string // Optional server-to-server callback URL
	Language    string // Language (FI, SV, EN)
}

// CreateSimplePayment creates a payment with simplified parameters
func (c *Client) CreateSimplePayment(ctx context.Context, req *SimplePaymentRequest) (*CreatePaymentResponse, error) {
	if req.Language == "" {
		req.Language = "FI"
	}

	// Build full request
	fullReq := &CreatePaymentRequest{
		Stamp:     req.Stamp,
		Reference: req.Reference,
		Amount:    req.Amount,
		Currency:  "EUR",
		Language:  req.Language,
		Items: []PaymentItem{
			{
				UnitPrice:     req.Amount,
				Units:         1,
				VATPercentage: 24, // Finnish VAT
				ProductCode:   "stream-access",
				Description:   req.Description,
				Stamp:         req.Stamp,
			},
		},
		Customer: Customer{
			Email: req.Email,
		},
		RedirectURLs: RedirectURLs{
			Success: req.SuccessURL,
			Cancel:  req.CancelURL,
		},
	}

	// Add callback URLs if provided
	if req.CallbackURL != "" {
		fullReq.CallbackURLs = &CallbackURLs{
			Success: req.CallbackURL,
			Cancel:  req.CallbackURL,
		}
	}

	return c.CreatePayment(ctx, fullReq)
}
