// Package paypal wraps the parts of the PayPal REST API that Mandate uses:
// OAuth2 client-credentials tokens and the Orders v2 API (create, get,
// capture). The Client interface lets the server swap in a Mock for tests and
// offline demos.
package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	SandboxBaseURL = "https://api-m.sandbox.paypal.com"
	LiveBaseURL    = "https://api-m.paypal.com"
)

// OrderItem is one purchase_units[].items[] entry.
type OrderItem struct {
	Name       string
	SKU        string
	Quantity   int
	UnitAmount float64
	Category   string // PHYSICAL_GOODS | DIGITAL_GOODS | DONATION
}

// CreateOrderRequest describes the order to create.
type CreateOrderRequest struct {
	Currency    string
	Amount      float64
	Description string
	CustomID    string // our proposal/mission reference, also used as idempotency key
	Items       []OrderItem
	ReturnURL   string
	CancelURL   string
	BrandName   string
	// StoreInVault asks PayPal to save the buyer's PayPal account when the
	// order is captured (payment_source.paypal.attributes.vault). The buyer
	// consents at checkout; the capture response then carries the vault id.
	StoreInVault bool
	// VaultID pays with a previously saved PayPal account instead of sending
	// the buyer to checkout. The order comes back approved and can be captured
	// straight away.
	VaultID string
	// RequestID overrides the PayPal-Request-Id idempotency key (defaults to
	// CustomID). Retries that change the request body must use a new key.
	RequestID string
}

// Order is the normalised view of a PayPal order.
type Order struct {
	ID         string  `json:"id"`
	Status     string  `json:"status"`
	ApproveURL string  `json:"approve_url,omitempty"`
	CaptureID  string  `json:"capture_id,omitempty"`
	Amount     float64 `json:"amount"`
	Currency   string  `json:"currency"`
	PayerEmail string  `json:"payer_email,omitempty"`
	// VaultID and VaultStatus describe the saved payment method PayPal
	// returned for this order (status VAULTED once the token is usable).
	VaultID     string          `json:"vault_id,omitempty"`
	VaultStatus string          `json:"vault_status,omitempty"`
	Raw         json.RawMessage `json:"-"`
}

// Client is implemented by the real API client and the Mock.
type Client interface {
	CreateOrder(ctx context.Context, req CreateOrderRequest) (*Order, error)
	CaptureOrder(ctx context.Context, orderID string) (*Order, error)
	GetOrder(ctx context.Context, orderID string) (*Order, error)
	// VerifyWebhook checks a webhook delivery's signature with PayPal
	// (POST /v1/notifications/verify-webhook-signature). webhookID is the ID
	// of the webhook configured in the Developer Dashboard.
	VerifyWebhook(ctx context.Context, webhookID string, headers http.Header, body []byte) (bool, error)
	Mode() string // "sandbox", "live" or "mock"
}

// WebhookEvent is the subset of a PayPal webhook payload Mandate acts on.
type WebhookEvent struct {
	ID           string `json:"id"`
	EventType    string `json:"event_type"`
	ResourceType string `json:"resource_type"`
	Resource     struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Amount struct {
			CurrencyCode string `json:"currency_code"`
			Value        string `json:"value"`
		} `json:"amount"`
		CustomID          string `json:"custom_id"`
		SupplementaryData struct {
			RelatedIDs struct {
				OrderID string `json:"order_id"`
			} `json:"related_ids"`
		} `json:"supplementary_data"`
		// Payment-token events (VAULT.PAYMENT-TOKEN.CREATED) carry the order
		// that saved the account and the account's email.
		Metadata struct {
			OrderID string `json:"order_id"`
		} `json:"metadata"`
		PaymentSource struct {
			PayPal struct {
				EmailAddress string `json:"email_address"`
			} `json:"paypal"`
		} `json:"payment_source"`
	} `json:"resource"`
}

// OrderID returns the checkout order an event refers to, for both order
// events (resource is the order) and capture events (resource is the capture).
func (e WebhookEvent) OrderID() string {
	if e.Resource.SupplementaryData.RelatedIDs.OrderID != "" {
		return e.Resource.SupplementaryData.RelatedIDs.OrderID
	}
	if e.Resource.Metadata.OrderID != "" {
		return e.Resource.Metadata.OrderID
	}
	if e.ResourceType == "checkout-order" || strings.HasPrefix(e.EventType, "CHECKOUT.ORDER.") {
		return e.Resource.ID
	}
	return ""
}

// APIError carries the PayPal error payload.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string { return fmt.Sprintf("paypal: HTTP %d: %s", e.Status, e.Body) }

// REST talks to the PayPal REST API.
type REST struct {
	ClientID string
	Secret   string
	BaseURL  string
	HTTP     *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewREST builds a client for the sandbox or live environment.
func NewREST(clientID, secret, baseURL string) *REST {
	if baseURL == "" {
		baseURL = SandboxBaseURL
	}
	return &REST{ClientID: clientID, Secret: secret, BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Mode implements Client.
func (r *REST) Mode() string {
	if r.BaseURL == LiveBaseURL {
		return "live"
	}
	return "sandbox"
}

func (r *REST) accessToken(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.token != "" && time.Now().Before(r.tokenExp.Add(-60*time.Second)) {
		return r.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.BaseURL+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(r.ClientID, r.Secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("paypal: token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{Status: resp.StatusCode, Body: string(body)}
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("paypal: bad token response: %s", string(body))
	}
	r.token = tok.AccessToken
	r.tokenExp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return r.token, nil
}

func (r *REST) do(ctx context.Context, method, path string, body any, requestID string) (json.RawMessage, int, error) {
	tok, err := r.accessToken(ctx)
	if err != nil {
		return nil, 0, err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.BaseURL+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=representation")
	if requestID != "" {
		req.Header.Set("PayPal-Request-Id", requestID)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("paypal: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	return json.RawMessage(data), resp.StatusCode, nil
}

func money(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// cutBytes shortens s to at most max bytes without splitting a UTF-8 rune
// (PayPal limits are in bytes; summaries may contain accents or symbols).
func cutBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// CreateOrder implements Client using POST /v2/checkout/orders with
// intent=CAPTURE and the PayPal payment source so the buyer approves on
// PayPal and is sent back to ReturnURL.
func (r *REST) CreateOrder(ctx context.Context, req CreateOrderRequest) (*Order, error) {
	if req.Amount <= 0 {
		return nil, errors.New("paypal: amount must be positive")
	}
	cur := strings.ToUpper(req.Currency)
	if cur == "" {
		cur = "USD"
	}
	items := make([]map[string]any, 0, len(req.Items))
	itemTotal := 0.0
	for _, it := range req.Items {
		q := it.Quantity
		if q <= 0 {
			q = 1
		}
		cat := it.Category
		if cat == "" {
			cat = "PHYSICAL_GOODS"
		}
		name := cutBytes(it.Name, 127)
		items = append(items, map[string]any{
			"name":        name,
			"sku":         it.SKU,
			"quantity":    strconv.Itoa(q),
			"category":    cat,
			"unit_amount": map[string]string{"currency_code": cur, "value": money(it.UnitAmount)},
		})
		itemTotal += it.UnitAmount * float64(q)
	}
	amount := map[string]any{"currency_code": cur, "value": money(req.Amount)}
	if len(items) > 0 && money(itemTotal) == money(req.Amount) {
		amount["breakdown"] = map[string]any{"item_total": map[string]string{"currency_code": cur, "value": money(itemTotal)}}
	} else {
		items = nil // PayPal rejects item lists whose total does not match the breakdown
	}
	desc := cutBytes(req.Description, 127)
	pu := map[string]any{"amount": amount, "description": desc}
	if req.CustomID != "" {
		pu["custom_id"] = req.CustomID
	}
	if items != nil {
		pu["items"] = items
	}
	brand := req.BrandName
	if brand == "" {
		brand = "Mandate"
	}
	source := map[string]any{}
	requestID := req.RequestID
	if requestID == "" {
		requestID = req.CustomID
	}
	if req.VaultID != "" {
		// Pay with the saved account: no redirect, no buyer interaction.
		source["vault_id"] = req.VaultID
		if requestID != "" {
			requestID += "-vault"
		}
	} else {
		source["experience_context"] = map[string]any{
			"brand_name":                brand,
			"user_action":               "PAY_NOW",
			"shipping_preference":       "NO_SHIPPING",
			"landing_page":              "LOGIN",
			"payment_method_preference": "IMMEDIATE_PAYMENT_REQUIRED", // no pending e-check payments; the capture is final
			"return_url":                req.ReturnURL,
			"cancel_url":                req.CancelURL,
		}
		if req.StoreInVault {
			source["attributes"] = map[string]any{
				"vault": map[string]any{
					"store_in_vault": "ON_SUCCESS",
					"usage_type":     "MERCHANT",
					"customer_type":  "CONSUMER",
				},
			}
			if requestID != "" {
				requestID += "-save"
			}
		}
	}
	body := map[string]any{
		"intent":         "CAPTURE",
		"purchase_units": []any{pu},
		"payment_source": map[string]any{"paypal": source},
	}
	raw, _, err := r.do(ctx, http.MethodPost, "/v2/checkout/orders", body, requestID)
	if err != nil {
		return nil, err
	}
	return parseOrder(raw)
}

// CaptureOrder implements Client using POST /v2/checkout/orders/{id}/capture.
func (r *REST) CaptureOrder(ctx context.Context, orderID string) (*Order, error) {
	raw, _, err := r.do(ctx, http.MethodPost, "/v2/checkout/orders/"+url.PathEscape(orderID)+"/capture", map[string]any{}, "capture-"+orderID)
	if err != nil {
		var apiErr *APIError
		// An ORDER_ALREADY_CAPTURED error means the buyer hit the return URL twice; treat it as success.
		if errors.As(err, &apiErr) && strings.Contains(apiErr.Body, "ORDER_ALREADY_CAPTURED") {
			return r.GetOrder(ctx, orderID)
		}
		return nil, err
	}
	return parseOrder(raw)
}

// VerifyWebhook implements Client.
func (r *REST) VerifyWebhook(ctx context.Context, webhookID string, h http.Header, body []byte) (bool, error) {
	if webhookID == "" {
		return false, errors.New("paypal: PAYPAL_WEBHOOK_ID is not configured")
	}
	var event json.RawMessage = body
	req := map[string]any{
		"auth_algo":         h.Get("Paypal-Auth-Algo"),
		"cert_url":          h.Get("Paypal-Cert-Url"),
		"transmission_id":   h.Get("Paypal-Transmission-Id"),
		"transmission_sig":  h.Get("Paypal-Transmission-Sig"),
		"transmission_time": h.Get("Paypal-Transmission-Time"),
		"webhook_id":        webhookID,
		"webhook_event":     event,
	}
	raw, _, err := r.do(ctx, http.MethodPost, "/v1/notifications/verify-webhook-signature", req, "")
	if err != nil {
		return false, err
	}
	var out struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, err
	}
	return out.VerificationStatus == "SUCCESS", nil
}

// GetOrder implements Client using GET /v2/checkout/orders/{id}.
func (r *REST) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	raw, _, err := r.do(ctx, http.MethodGet, "/v2/checkout/orders/"+url.PathEscape(orderID), nil, "")
	if err != nil {
		return nil, err
	}
	return parseOrder(raw)
}

type vaultAttrs struct {
	Vault struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"vault"`
}

func parseOrder(raw json.RawMessage) (*Order, error) {
	var o struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Links  []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
		PurchaseUnits []struct {
			Amount struct {
				CurrencyCode string `json:"currency_code"`
				Value        string `json:"value"`
			} `json:"amount"`
			Payments struct {
				Captures []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Amount struct {
						CurrencyCode string `json:"currency_code"`
						Value        string `json:"value"`
					} `json:"amount"`
				} `json:"captures"`
			} `json:"payments"`
		} `json:"purchase_units"`
		Payer struct {
			EmailAddress string `json:"email_address"`
		} `json:"payer"`
		PaymentSource struct {
			PayPal struct {
				EmailAddress string     `json:"email_address"`
				Attributes   vaultAttrs `json:"attributes"`
				Attribute    vaultAttrs `json:"attribute"` // PayPal's docs show both spellings
			} `json:"paypal"`
		} `json:"payment_source"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("paypal: decode order: %w", err)
	}
	out := &Order{ID: o.ID, Status: o.Status, Raw: raw, PayerEmail: o.Payer.EmailAddress}
	if out.PayerEmail == "" {
		out.PayerEmail = o.PaymentSource.PayPal.EmailAddress
	}
	v := o.PaymentSource.PayPal.Attributes.Vault
	if v.ID == "" {
		v = o.PaymentSource.PayPal.Attribute.Vault
	}
	out.VaultID, out.VaultStatus = v.ID, v.Status
	for _, l := range o.Links {
		if l.Rel == "payer-action" || l.Rel == "approve" {
			out.ApproveURL = l.Href
		}
	}
	if len(o.PurchaseUnits) > 0 {
		pu := o.PurchaseUnits[0]
		out.Currency = pu.Amount.CurrencyCode
		out.Amount, _ = strconv.ParseFloat(pu.Amount.Value, 64)
		if len(pu.Payments.Captures) > 0 {
			c := pu.Payments.Captures[0]
			out.CaptureID = c.ID
			if c.Amount.Value != "" {
				out.Amount, _ = strconv.ParseFloat(c.Amount.Value, 64)
				out.Currency = c.Amount.CurrencyCode
			}
		}
	}
	return out, nil
}
