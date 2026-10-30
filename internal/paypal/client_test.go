package paypal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestVerifyWebhook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			_, _ = io.WriteString(w, `{"access_token":"t","expires_in":3600}`)
		case "/v1/notifications/verify-webhook-signature":
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if m["webhook_id"] != "WH-ID" || m["transmission_id"] != "tid" || m["webhook_event"] == nil {
				http.Error(w, "bad verify request: "+string(body), 400)
				return
			}
			_, _ = io.WriteString(w, `{"verification_status":"SUCCESS"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewREST("a", "b", srv.URL)
	h := http.Header{}
	h.Set("Paypal-Transmission-Id", "tid")
	h.Set("Paypal-Transmission-Sig", "sig")
	h.Set("Paypal-Cert-Url", "https://api.sandbox.paypal.com/cert")
	h.Set("Paypal-Auth-Algo", "SHA256withRSA")
	h.Set("Paypal-Transmission-Time", "2026-10-30T12:00:00Z")
	ok, err := c.VerifyWebhook(context.Background(), "WH-ID", h, []byte(`{"id":"WH-1","event_type":"CHECKOUT.ORDER.APPROVED"}`))
	if err != nil || !ok {
		t.Fatalf("verify: ok=%v err=%v", ok, err)
	}
	if _, err := c.VerifyWebhook(context.Background(), "", h, nil); err == nil {
		t.Fatal("expected error without webhook id")
	}
	ev := WebhookEvent{EventType: "PAYMENT.CAPTURE.COMPLETED", ResourceType: "capture"}
	ev.Resource.ID = "CAP1"
	ev.Resource.SupplementaryData.RelatedIDs.OrderID = "ORD1"
	if ev.OrderID() != "ORD1" {
		t.Fatalf("capture event order id = %q", ev.OrderID())
	}
	ev2 := WebhookEvent{EventType: "CHECKOUT.ORDER.APPROVED", ResourceType: "checkout-order"}
	ev2.Resource.ID = "ORD2"
	if ev2.OrderID() != "ORD2" {
		t.Fatalf("order event order id = %q", ev2.OrderID())
	}
}

func TestCreateOrderVaultBodies(t *testing.T) {
	var bodies []map[string]any
	var requestIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			_, _ = io.WriteString(w, `{"access_token":"t","expires_in":3600}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		bodies = append(bodies, m)
		requestIDs = append(requestIDs, r.Header.Get("PayPal-Request-Id"))
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{"id":"A","status":"PAYER_ACTION_REQUIRED","links":[{"rel":"payer-action","href":"https://approve"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"B","status":"APPROVED","payment_source":{"paypal":{"email_address":"buyer@example.com","attributes":{"vault":{"id":"v1","status":"VAULTED"}}}}}`)
	}))
	defer srv.Close()
	c := NewREST("a", "b", srv.URL)
	// 1. a checkout that asks PayPal to save the account
	o, err := c.CreateOrder(context.Background(), CreateOrderRequest{Currency: "USD", Amount: 10, CustomID: "prop_1", ReturnURL: "https://x/return", CancelURL: "https://x/cancel", StoreInVault: true})
	if err != nil || o.ApproveURL != "https://approve" {
		t.Fatalf("save order: %v %+v", err, o)
	}
	pp := bodies[0]["payment_source"].(map[string]any)["paypal"].(map[string]any)
	vault := pp["attributes"].(map[string]any)["vault"].(map[string]any)
	if vault["store_in_vault"] != "ON_SUCCESS" || vault["usage_type"] != "MERCHANT" || pp["experience_context"] == nil {
		t.Fatalf("vault attributes missing: %v", pp)
	}
	if requestIDs[0] != "prop_1-save" {
		t.Fatalf("request id = %q", requestIDs[0])
	}
	// 2. paying with the saved account: no redirect, vault_id in the body
	o, err = c.CreateOrder(context.Background(), CreateOrderRequest{Currency: "USD", Amount: 10, CustomID: "prop_2", VaultID: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	pp = bodies[1]["payment_source"].(map[string]any)["paypal"].(map[string]any)
	if pp["vault_id"] != "v1" || pp["experience_context"] != nil || pp["attributes"] != nil {
		t.Fatalf("vault body wrong: %v", pp)
	}
	if requestIDs[1] != "prop_2-vault" {
		t.Fatalf("request id = %q", requestIDs[1])
	}
	if o.Status != "APPROVED" || o.ApproveURL != "" || o.VaultID != "v1" || o.VaultStatus != "VAULTED" || o.PayerEmail != "buyer@example.com" {
		t.Fatalf("parsed vault order: %+v", o)
	}
}

func TestParseOrderCaptureWithVault(t *testing.T) {
	raw := `{"id":"X","status":"COMPLETED","payer":{"email_address":"p@example.com"},
	  "payment_source":{"paypal":{"attributes":{"vault":{"id":"tok","status":"VAULTED","customer":{"id":"c1"}}}}},
	  "purchase_units":[{"amount":{"currency_code":"USD","value":"12.50"},"payments":{"captures":[{"id":"CAP1","status":"COMPLETED","amount":{"currency_code":"USD","value":"12.50"}}]}}]}`
	o, err := parseOrder(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if o.CaptureID != "CAP1" || o.VaultID != "tok" || o.VaultStatus != "VAULTED" || o.PayerEmail != "p@example.com" || o.Amount != 12.5 {
		t.Fatalf("parsed: %+v", o)
	}
}

func TestMockVault(t *testing.T) {
	m := NewMock("http://localhost:8080")
	// unknown token is refused like PayPal does
	if _, err := m.CreateOrder(context.Background(), CreateOrderRequest{Currency: "USD", Amount: 5, VaultID: "nope"}); err == nil {
		t.Fatal("unknown vault id should fail")
	}
	o, _ := m.CreateOrder(context.Background(), CreateOrderRequest{Currency: "USD", Amount: 10, ReturnURL: "http://x/r", CancelURL: "http://x/c", StoreInVault: true})
	_, _ = m.Approve(o.ID, true)
	c, err := m.CaptureOrder(context.Background(), o.ID)
	if err != nil || c.VaultID == "" || c.VaultStatus != "VAULTED" || !m.Vaulted(c.VaultID) {
		t.Fatalf("capture should save the account: %v %+v", err, c)
	}
	o2, err := m.CreateOrder(context.Background(), CreateOrderRequest{Currency: "USD", Amount: 7, VaultID: c.VaultID})
	if err != nil || o2.Status != "APPROVED" || o2.ApproveURL != "" || o2.PayerEmail == "" {
		t.Fatalf("vault order: %v %+v", err, o2)
	}
	c2, err := m.CaptureOrder(context.Background(), o2.ID)
	if err != nil || c2.Status != "COMPLETED" || c2.CaptureID == "" {
		t.Fatalf("vault capture: %v %+v", err, c2)
	}
	m.Revoke(c.VaultID)
	if _, err := m.CreateOrder(context.Background(), CreateOrderRequest{Currency: "USD", Amount: 7, VaultID: c.VaultID}); err == nil {
		t.Fatal("revoked token should fail")
	}
}

func TestCutBytesKeepsRunesWhole(t *testing.T) {
	s := strings.Repeat("é", 70) // 140 bytes
	out := cutBytes(s, 127)
	if len(out) > 127 || !utf8.ValidString(out) || out != strings.Repeat("é", 63) {
		t.Fatalf("cut = %q (%d bytes)", out, len(out))
	}
	if cutBytes("short", 127) != "short" {
		t.Fatal("short strings must pass through")
	}
}
