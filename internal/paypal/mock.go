package paypal

import (
	"context"
	"fmt"
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

	mu     sync.Mutex
	orders map[string]*mockOrder
	seq    int
}

type mockOrder struct {
	Order
	ReturnURL string
	CancelURL string
	Items     []OrderItem
	Approved  bool
}

// NewMock returns an empty simulator.
func NewMock(publicURL string) *Mock {
	return &Mock{PublicURL: strings.TrimRight(publicURL, "/"), orders: map[string]*mockOrder{}}
}

// Mode implements Client.
func (m *Mock) Mode() string { return "mock" }

// CreateOrder implements Client.
func (m *Mock) CreateOrder(_ context.Context, req CreateOrderRequest) (*Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	id := fmt.Sprintf("MOCK%d%06d", time.Now().Unix()%100000, m.seq)
	o := &mockOrder{Order: Order{ID: id, Status: "PAYER_ACTION_REQUIRED", Amount: req.Amount, Currency: strings.ToUpper(req.Currency)}, ReturnURL: req.ReturnURL, CancelURL: req.CancelURL, Items: req.Items}
	o.ApproveURL = fmt.Sprintf("%s/mock/paypal/approve?token=%s", m.PublicURL, id)
	m.orders[id] = o
	out := o.Order
	return &out, nil
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
	if o.Status == "COMPLETED" {
		out := o.Order
		return &out, nil
	}
	if !o.Approved {
		return nil, &APIError{Status: 422, Body: `{"name":"UNPROCESSABLE_ENTITY","details":[{"issue":"ORDER_NOT_APPROVED"}]}`}
	}
	o.Status = "COMPLETED"
	o.CaptureID = "CAP" + o.ID[4:]
	out := o.Order
	return &out, nil
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
