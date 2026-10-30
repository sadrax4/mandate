package paypal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRESTOrdersFlow(t *testing.T) {
	var gotCreate map[string]any
	tokens := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			u, p, ok := r.BasicAuth()
			if !ok || u != "cid" || p != "sec" {
				http.Error(w, "bad auth", 401)
				return
			}
			tokens++
			_, _ = io.WriteString(w, `{"access_token":"tok123","expires_in":3600}`)
		case "/v2/checkout/orders":
			if r.Header.Get("Authorization") != "Bearer tok123" {
				http.Error(w, "no token", 401)
				return
			}
			if r.Header.Get("PayPal-Request-Id") != "prop_1" {
				http.Error(w, "missing idempotency key", 400)
				return
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotCreate)
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"5O190127TN364715T","status":"PAYER_ACTION_REQUIRED","links":[{"rel":"self","href":"x"},{"rel":"payer-action","href":"https://www.sandbox.paypal.com/checkoutnow?token=5O190127TN364715T"}],"purchase_units":[{"amount":{"currency_code":"USD","value":"159.99"}}]}`)
		case "/v2/checkout/orders/5O190127TN364715T/capture":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"5O190127TN364715T","status":"COMPLETED","payer":{"email_address":"buyer@example.com"},"purchase_units":[{"amount":{"currency_code":"USD","value":"159.99"},"payments":{"captures":[{"id":"3C679366HH908993F","status":"COMPLETED","amount":{"currency_code":"USD","value":"159.99"}}]}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewREST("cid", "sec", srv.URL)
	ctx := context.Background()
	o, err := c.CreateOrder(ctx, CreateOrderRequest{Currency: "usd", Amount: 159.99, CustomID: "prop_1", Description: "test", ReturnURL: "http://x/r", CancelURL: "http://x/c",
		Items: []OrderItem{{Name: "Keyboard", SKU: "kb-1", Quantity: 1, UnitAmount: 109.99}, {Name: "Hub", SKU: "hub-1", Quantity: 1, UnitAmount: 50.00}}})
	if err != nil {
		t.Fatal(err)
	}
	if o.ID != "5O190127TN364715T" || o.ApproveURL == "" || o.Amount != 159.99 {
		t.Fatalf("unexpected order %+v", o)
	}
	if gotCreate["intent"] != "CAPTURE" {
		t.Fatalf("intent = %v", gotCreate["intent"])
	}
	pu := gotCreate["purchase_units"].([]any)[0].(map[string]any)
	amt := pu["amount"].(map[string]any)
	if amt["value"] != "159.99" || amt["currency_code"] != "USD" {
		t.Fatalf("amount = %v", amt)
	}
	if amt["breakdown"].(map[string]any)["item_total"].(map[string]any)["value"] != "159.99" {
		t.Fatalf("breakdown = %v", amt["breakdown"])
	}
	if len(pu["items"].([]any)) != 2 {
		t.Fatalf("items = %v", pu["items"])
	}
	ps := gotCreate["payment_source"].(map[string]any)["paypal"].(map[string]any)["experience_context"].(map[string]any)
	if ps["return_url"] != "http://x/r" || ps["user_action"] != "PAY_NOW" {
		t.Fatalf("experience_context = %v", ps)
	}
	cap, err := c.CaptureOrder(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cap.Status != "COMPLETED" || cap.CaptureID != "3C679366HH908993F" || cap.PayerEmail != "buyer@example.com" {
		t.Fatalf("capture = %+v", cap)
	}
	if tokens != 1 {
		t.Fatalf("token should be cached, fetched %d times", tokens)
	}
}

func TestCreateOrderDropsMismatchedItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			_, _ = io.WriteString(w, `{"access_token":"t","expires_in":3600}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		pu := m["purchase_units"].([]any)[0].(map[string]any)
		if _, ok := pu["items"]; ok {
			http.Error(w, "items should have been dropped", 400)
			return
		}
		_, _ = io.WriteString(w, `{"id":"A","status":"CREATED","links":[{"rel":"approve","href":"https://approve"}]}`)
	}))
	defer srv.Close()
	c := NewREST("a", "b", srv.URL)
	o, err := c.CreateOrder(context.Background(), CreateOrderRequest{Currency: "USD", Amount: 100, Items: []OrderItem{{Name: "x", Quantity: 1, UnitAmount: 90}}})
	if err != nil {
		t.Fatal(err)
	}
	if o.ApproveURL != "https://approve" {
		t.Fatalf("approve url = %q", o.ApproveURL)
	}
}

func TestMock(t *testing.T) {
	m := NewMock("http://localhost:8080")
	o, err := m.CreateOrder(context.Background(), CreateOrderRequest{Currency: "USD", Amount: 10, ReturnURL: "http://localhost:8080/paypal/return", CancelURL: "http://localhost:8080/paypal/cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CaptureOrder(context.Background(), o.ID); err == nil {
		t.Fatal("capture before approval should fail")
	}
	next, err := m.Approve(o.ID, true)
	if err != nil || next != "http://localhost:8080/paypal/return?token="+o.ID {
		t.Fatalf("approve: %v %q", err, next)
	}
	c, err := m.CaptureOrder(context.Background(), o.ID)
	if err != nil || c.Status != "COMPLETED" || c.CaptureID == "" {
		t.Fatalf("capture: %v %+v", err, c)
	}
}
