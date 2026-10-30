package paypal

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Mock simulates the Orders API in memory. CreateOrder returns an approve URL
// that points at the local approval simulator page (served by the HTTP server
// at /mock/paypal/approve), which then redirects to ReturnURL just like the
// real PayPal checkout does.
type Mock struct {
	PublicURL string // base URL of our own server, used to build approve links
	// VaultCompletesOnCreate makes vault_id orders come back COMPLETED with a
	// capture at creation, which PayPal does for saved PayPal accounts.
	VaultCompletesOnCreate bool
	// OnCreate, when set, runs during CreateOrder before the order exists
	// (tests use it to change state while PayPal is "slow").
	OnCreate func(req CreateOrderRequest)
	// NextCaptureStatus, when set, is the status of the next capture
	// (DECLINED, PENDING, ...) instead of COMPLETED; PayPal still reports
	// the order itself as COMPLETED. It is consumed by one capture.
	NextCaptureStatus string

	mu     sync.Mutex
	orders map[string]*mockOrder
	vaults map[string]string // vault id -> payer email, like PayPal's saved payment methods
	seq    int
}

type mockOrder struct {
	Order
	ReturnURL    string
	CancelURL    string
	Items        []OrderItem
	Approved     bool
	StoreInVault bool
}

// NewMock returns an empty simulator.
func NewMock(publicURL string) *Mock {
	return &Mock{PublicURL: strings.TrimRight(publicURL, "/"), orders: map[string]*mockOrder{}, vaults: map[string]string{}}
}

// Mode implements Client.
func (m *Mock) Mode() string { return "mock" }

// CreateOrder implements Client.
func (m *Mock) CreateOrder(_ context.Context, req CreateOrderRequest) (*Order, error) {
	if m.OnCreate != nil {
		m.OnCreate(req)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	id := fmt.Sprintf("MOCK%d%06d", time.Now().Unix()%100000, m.seq)
	o := &mockOrder{Order: Order{ID: id, Status: "PAYER_ACTION_REQUIRED", Amount: req.Amount, Currency: strings.ToUpper(req.Currency)}, ReturnURL: req.ReturnURL, CancelURL: req.CancelURL, Items: req.Items, StoreInVault: req.StoreInVault}
	if req.VaultID != "" {
		// Paying with a saved account: PayPal returns the order already approved.
		payer, ok := m.vaults[req.VaultID]
		if !ok {
			return nil, &APIError{Status: 422, Body: `{"name":"UNPROCESSABLE_ENTITY","details":[{"issue":"INVALID_RESOURCE_ID","description":"Specified vault_id does not exist."}]}`}
		}
		o.Status = "APPROVED"
		o.Approved = true
		o.PayerEmail = payer
		o.VaultID = req.VaultID
		o.VaultStatus = "VAULTED"
		if m.VaultCompletesOnCreate {
			o.Status = "COMPLETED"
			o.CaptureID = "CAP" + o.ID[4:]
			o.CaptureStatus = "COMPLETED"
			if m.NextCaptureStatus != "" {
				o.CaptureStatus, m.NextCaptureStatus = m.NextCaptureStatus, ""
			}
		}
	} else {
		o.ApproveURL = fmt.Sprintf("%s/mock/paypal/approve?token=%s", m.PublicURL, id)
	}
	m.orders[id] = o
	out := o.Order
	return &out, nil
}

// Revoke deletes a saved payment method, as a buyer removing the merchant's
// permission in their PayPal account would (tests).
func (m *Mock) Revoke(vaultID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.vaults, vaultID)
}

// Vaulted reports whether a saved payment method exists (tests).
func (m *Mock) Vaulted(vaultID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.vaults[vaultID]
	return ok
}

// Approve marks the order as approved by the buyer (the simulator calls it).
// It returns the URL the buyer should be redirected to.
func (m *Mock) Approve(orderID string, approve bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orders[orderID]
	if !ok {
		return "", fmt.Errorf("paypal mock: unknown order %s", orderID)
	}
	if !approve {
		o.Status = "VOIDED"
		return appendToken(o.CancelURL, orderID), nil
	}
	o.Approved = true
	o.Status = "APPROVED"
	o.PayerEmail = "sb-buyer@personal.example.com"
	return appendToken(o.ReturnURL, orderID), nil
}

// SetStatus overrides an order's status (tests simulate VOIDED/EXPIRED orders).
func (m *Mock) SetStatus(orderID, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o, ok := m.orders[orderID]; ok {
		o.Status = status
	}
}

// Details returns what the simulator page shows the buyer.
func (m *Mock) Details(orderID string) (*Order, []OrderItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orders[orderID]
	if !ok {
		return nil, nil, fmt.Errorf("paypal mock: unknown order %s", orderID)
	}
	out := o.Order
	return &out, o.Items, nil
}

// CaptureOrder implements Client.
func (m *Mock) CaptureOrder(_ context.Context, orderID string) (*Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orders[orderID]
	if !ok {
		return nil, &APIError{Status: 404, Body: `{"name":"RESOURCE_NOT_FOUND"}`}
	}
	if o.CaptureID != "" {
		// PayPal answers ORDER_ALREADY_CAPTURED; REST.CaptureOrder turns that
		// into a re-read of the order, so the simulator does the same.
		out := o.Order
		return &out, nil
	}
	if !o.Approved {
		return nil, &APIError{Status: 422, Body: `{"name":"UNPROCESSABLE_ENTITY","details":[{"issue":"ORDER_NOT_APPROVED"}]}`}
	}
	o.Status = "COMPLETED"
	o.CaptureID = "CAP" + o.ID[4:]
	o.CaptureStatus = "COMPLETED"
	if m.NextCaptureStatus != "" {
		o.CaptureStatus, m.NextCaptureStatus = m.NextCaptureStatus, ""
	}
	if o.StoreInVault && o.VaultID == "" {
		// The buyer consented at checkout; PayPal returns the saved account with the capture.
		o.VaultID = "VLT" + o.ID[4:]
		o.VaultStatus = "VAULTED"
		m.vaults[o.VaultID] = o.PayerEmail
	}
	out := o.Order
	return &out, nil
}

// VerifyWebhook implements Client. The simulator accepts deliveries that carry
// the shared secret "mock" in the Paypal-Transmission-Sig header, which is what
// the tests send.
func (m *Mock) VerifyWebhook(_ context.Context, _ string, h http.Header, _ []byte) (bool, error) {
	return h.Get("Paypal-Transmission-Sig") == "mock", nil
}

// Drop forgets an order, as PayPal does with old ones (tests).
func (m *Mock) Drop(orderID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.orders, orderID)
}

// SetCaptureStatus changes a captured order's capture status, as PayPal does
// when a pending capture completes.
func (m *Mock) SetCaptureStatus(orderID, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o, ok := m.orders[orderID]; ok && o.CaptureID != "" {
		o.CaptureStatus = status
	}
}

// GetOrder implements Client.
func (m *Mock) GetOrder(_ context.Context, orderID string) (*Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orders[orderID]
	if !ok {
		return nil, &APIError{Status: 404, Body: `{"name":"RESOURCE_NOT_FOUND"}`}
	}
	out := o.Order
	return &out, nil
}

func appendToken(u, token string) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "token=" + token
}
