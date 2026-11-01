package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"mandate/internal/agent"
	"mandate/internal/catalog"
	"mandate/internal/mandate"
	"mandate/internal/paypal"
	"mandate/internal/store"
)

type env struct {
	t   *testing.T
	ts  *httptest.Server
	st  *store.Store
	cli *http.Client
	pp  *paypal.Mock
	srv *Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, _ := store.Open("")
	cat := catalog.NewMock()
	shopper := agent.NewShopper(agent.Fake{}, cat, st)
	var srv *Server
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	pp := paypal.NewMock(ts.URL)
	srv = New(Config{PublicURL: ts.URL, StaticDir: "/nonexistent", Currency: "USD"}, st, pp, shopper, cat)
	ts.Config.Handler = srv.Handler()
	t.Cleanup(ts.Close)
	t.Cleanup(srv.Close)
	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &env{t: t, ts: ts, st: st, cli: cli, pp: pp, srv: srv}
}

// startAutoApproved creates a mandate with an auto-approve threshold and a
// mission whose cart falls under it, so an order exists immediately.
func (e *env) startAutoApproved() (mandateID, missionID, orderID string) {
	e.t.Helper()
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Petty cash", "total_budget": 100, "auto_approve_under": 60}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingPayment)
	return md["id"].(string), ms["id"].(string), out["payments"].([]any)[0].(map[string]any)["id"].(string)
}

// str reads a JSON string field; a missing field (omitempty) reads as "".
func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func (e *env) mandateNumbers(id string) (spent, reserved float64) {
	var mdv map[string]any
	e.get("/api/mandates/"+id, &mdv)
	m := mdv["mandate"].(map[string]any)
	return m["spent"].(float64), m["reserved"].(float64)
}

// tryPost is post for goroutines: it returns the transport error instead of
// failing the test from a non-test goroutine.
func (e *env) tryPost(path string, body any, out any) (int, error) {
	b, _ := json.Marshal(body)
	resp, err := e.cli.Post(e.ts.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

func (e *env) post(path string, body any, out any) int {
	e.t.Helper()
	b, _ := json.Marshal(body)
	resp, err := e.cli.Post(e.ts.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (e *env) get(path string, out any) int {
	e.t.Helper()
	resp, err := e.cli.Get(e.ts.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (e *env) waitMission(id, status string) map[string]any {
	e.t.Helper()
	return e.waitMissionOneOf(id, status)
}

// waitMissionOneOf polls until the mission reaches one of the statuses.
func (e *env) waitMissionOneOf(id string, statuses ...string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var out map[string]any
		e.get("/api/missions/"+id, &out)
		m := out["mission"].(map[string]any)
		for _, want := range statuses {
			if m["status"] == want {
				return out
			}
		}
		if m["status"] == store.MissionFailed {
			e.t.Fatalf("mission failed: %v", m["error"])
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("mission %s never reached %v", id, statuses)
	return nil
}

func TestFullFlowApproveAndPay(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	if code := e.post("/api/mandates", map[string]any{"name": "Desk refresh", "total_budget": 250, "max_per_item": 120, "allowed_categories": []string{"computers"}, "min_rating": 4}, &md); code != 201 {
		t.Fatalf("create mandate: %d %v", code, md)
	}
	var ms map[string]any
	if code := e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "mechanical keyboard with brown switches and a usb-c hub"}, &ms); code != 201 {
		t.Fatalf("create mission: %d %v", code, ms)
	}
	mid := ms["id"].(string)
	out := e.waitMission(mid, store.MissionAwaitingApproval)
	props := out["proposals"].([]any)
	if len(props) != 1 {
		t.Fatalf("proposals = %d", len(props))
	}
	pid := props[0].(map[string]any)["id"].(string)

	// SSE stream replays history
	resp, err := http.Get(e.ts.URL + "/api/missions/" + mid + "/events")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	resp.Body.Close()
	if !strings.Contains(string(buf[:n]), "event: status") {
		t.Fatalf("sse missing events: %q", buf[:n])
	}

	var ap map[string]any
	if code := e.post("/api/proposals/"+pid+"/approve", nil, &ap); code != 200 {
		t.Fatalf("approve: %d %v", code, ap)
	}
	approveURL := ap["approve_url"].(string)
	if !strings.Contains(approveURL, "/mock/paypal/approve?token=") {
		t.Fatalf("approve url = %s", approveURL)
	}
	// budget is reserved
	var mdv map[string]any
	e.get("/api/mandates/"+md["id"].(string), &mdv)
	if mdv["mandate"].(map[string]any)["reserved"].(float64) <= 0 {
		t.Fatal("budget not reserved")
	}
	// approving twice is idempotent
	if code := e.post("/api/proposals/"+pid+"/approve", nil, &ap); code != 200 {
		t.Fatalf("second approve: %d", code)
	}

	// buyer visits the simulator and approves
	if code := e.get(strings.TrimPrefix(approveURL, e.ts.URL), nil); code != 200 {
		t.Fatalf("mock page: %d", code)
	}
	token := approveURL[strings.LastIndex(approveURL, "=")+1:]
	form := url.Values{"token": {token}, "action": {"approve"}}
	r, err := e.cli.PostForm(e.ts.URL+"/mock/paypal/approve", form)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	loc := r.Header.Get("Location")
	if r.StatusCode != 302 || !strings.Contains(loc, "/paypal/return?token="+token) {
		t.Fatalf("simulator redirect: %d %s", r.StatusCode, loc)
	}
	// PayPal sends the buyer back; we capture
	r2, err := e.cli.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != 302 || !strings.Contains(r2.Header.Get("Location"), "payment=completed") {
		t.Fatalf("return redirect: %d %s", r2.StatusCode, r2.Header.Get("Location"))
	}
	out = e.waitMission(mid, store.MissionPaid)
	pays := out["payments"].([]any)
	if len(pays) != 1 || pays[0].(map[string]any)["status"] != "COMPLETED" || str(pays[0].(map[string]any), "capture_id") == "" {
		t.Fatalf("payments = %v", pays)
	}
	e.get("/api/mandates/"+md["id"].(string), &mdv)
	mm := mdv["mandate"].(map[string]any)
	if mm["reserved"].(float64) != 0 || mm["spent"].(float64) <= 0 {
		t.Fatalf("mandate accounting wrong: %v", mm)
	}
	var ledger []map[string]any
	e.get("/api/ledger", &ledger)
	if len(ledger) != 1 || ledger[0]["mandate_name"] != "Desk refresh" || ledger[0]["goal"] == "" {
		t.Fatalf("ledger = %v", ledger)
	}
	// approving a paid proposal again is harmless: the same order comes back, nothing is charged twice
	if code := e.post("/api/proposals/"+pid+"/approve", nil, &ap); code != 200 || ap["payment"].(map[string]any)["id"] != ledger[0]["id"] {
		t.Fatalf("re-approve paid: %d %v", code, ap)
	}
	if e.get("/api/ledger", &ledger); len(ledger) != 1 {
		t.Fatalf("re-approval created an order: %v", ledger)
	}
}

func TestRejectResumesAgent(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Audio", "total_budget": 400, "allowed_categories": []string{"audio"}}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "noise cancelling headphones"}, &ms)
	mid := ms["id"].(string)
	out := e.waitMission(mid, store.MissionAwaitingApproval)
	pid := out["proposals"].([]any)[0].(map[string]any)["id"].(string)
	var rj map[string]any
	if code := e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": "too expensive"}, &rj); code != 200 {
		t.Fatalf("reject: %d %v", code, rj)
	}
	out = e.waitMission(mid, store.MissionAwaitingApproval)
	props := out["proposals"].([]any)
	if len(props) != 2 {
		t.Fatalf("expected 2 proposals after rejection, got %d", len(props))
	}
	if props[0].(map[string]any)["status"] != store.ProposalRejected || props[0].(map[string]any)["feedback"] != "too expensive" {
		t.Fatalf("first proposal = %v", props[0])
	}
	if props[1].(map[string]any)["status"] != store.ProposalPending {
		t.Fatalf("second proposal = %v", props[1])
	}
}

// A rejection without a reason stays empty: nothing is put in the human's
// mouth, the agent still revises, and the mission list carries the amount
// of the proposal waiting for approval.
func TestRejectWithoutReasonAndListAmounts(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Audio", "total_budget": 400, "allowed_categories": []string{"audio"}}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "noise cancelling headphones"}, &ms)
	mid := ms["id"].(string)
	out := e.waitMission(mid, store.MissionAwaitingApproval)
	first := out["proposals"].([]any)[0].(map[string]any)
	var list []map[string]any
	e.get("/api/missions", &list)
	if len(list) != 1 || list[0]["id"] != mid || list[0]["proposal_total"] != first["decision"].(map[string]any)["total"] || list[0]["proposal_items"] != float64(1) || list[0]["proposal_first"] == "" {
		t.Fatalf("mission row = %v", list)
	}
	var rj map[string]any
	if code := e.post("/api/proposals/"+first["id"].(string)+"/reject", map[string]any{"reason": "   "}, &rj); code != 200 {
		t.Fatalf("reject: %d %v", code, rj)
	}
	out = e.waitMission(mid, store.MissionAwaitingApproval)
	props := out["proposals"].([]any)
	if len(props) != 2 {
		t.Fatalf("expected a revised proposal, got %d", len(props))
	}
	if fb, _ := props[0].(map[string]any)["feedback"].(string); fb != "" {
		t.Fatalf("feedback should stay empty, got %q", fb)
	}
	found := false
	for _, ev := range out["events"].([]any) {
		m := ev.(map[string]any)
		if m["type"] == "rejected" {
			found = true
			if m["message"] != "human rejected the proposal" {
				t.Fatalf("rejected event = %q", m["message"])
			}
		}
	}
	if !found {
		t.Fatal("no rejected event")
	}
	e.get("/api/missions", &list)
	if list[0]["proposal_total"] != props[1].(map[string]any)["decision"].(map[string]any)["total"] {
		t.Fatalf("row should follow the new proposal: %v", list[0])
	}
}

// The approval is part of the mission's story, and a cart waiting for
// approval is re-checked against the mandate as it is now.
func TestApprovalEventAndLiveDecision(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Audio", "total_budget": 400, "allowed_categories": []string{"audio"}}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "noise cancelling headphones"}, &ms)
	mid := ms["id"].(string)
	out := e.waitMission(mid, store.MissionAwaitingApproval)
	pid := out["proposals"].([]any)[0].(map[string]any)["id"].(string)
	live, _ := out["live_decisions"].(map[string]any)
	if d, ok := live[pid].(map[string]any); !ok || d["allowed"] != true {
		t.Fatalf("live decision for the pending cart = %v", out["live_decisions"])
	}
	var list []map[string]any
	e.get("/api/missions", &list)
	if list[0]["fits"] != true {
		t.Fatalf("row should say the cart fits: %v", list[0])
	}
	var ap map[string]any
	if code := e.post("/api/proposals/"+pid+"/approve", nil, &ap); code != 200 {
		t.Fatalf("approve: %d %v", code, ap)
	}
	var d map[string]any
	e.get("/api/missions/"+mid, &d)
	found := false
	for _, ev := range d["events"].([]any) {
		m := ev.(map[string]any)
		if m["type"] == "approved" {
			found = m["data"].(map[string]any)["by"] == "human"
		}
	}
	if !found {
		t.Fatal("no approved event credited to the human")
	}
	var after []map[string]any
	e.get("/api/missions", &after)
	if after[0]["payment_status"] == "" || after[0]["payment_status"] == nil || after[0]["fits"] != nil {
		t.Fatalf("row after approval = %v", after[0])
	}
}

func TestAutoApproveGoesStraightToPayPal(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Petty cash", "total_budget": 100, "auto_approve_under": 60}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingPayment)
	pays := out["payments"].([]any)
	if len(pays) != 1 || pays[0].(map[string]any)["approve_url"] == "" {
		t.Fatalf("expected an order with approve url, got %v", pays)
	}
}

func TestCancelReleasesReservation(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Petty cash", "total_budget": 100, "auto_approve_under": 60}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingPayment)
	orderID := out["payments"].([]any)[0].(map[string]any)["id"].(string)
	r, _ := e.cli.Get(fmt.Sprintf("%s/paypal/cancel?token=%s", e.ts.URL, orderID))
	r.Body.Close()
	e.waitMission(ms["id"].(string), store.MissionCancelled)
	var mdv map[string]any
	e.get("/api/mandates/"+md["id"].(string), &mdv)
	if mdv["mandate"].(map[string]any)["reserved"].(float64) != 0 {
		t.Fatalf("reservation not released: %v", mdv)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	var out map[string]any
	if code := e.post("/api/mandates", map[string]any{"name": "", "total_budget": 10}, &out); code != 400 {
		t.Fatalf("expected 400, got %d", code)
	}
	if code := e.post("/api/missions", map[string]any{"mandate_id": "nope", "goal": "x"}, &out); code != 404 {
		t.Fatalf("expected 404, got %d", code)
	}
	if code := e.get("/api/missions/nope", &out); code != 404 {
		t.Fatalf("expected 404, got %d", code)
	}
	if code := e.get("/api/products/search?q=kettle&max_price=70", &out); code != 200 {
		t.Fatalf("search: %d", code)
	}
}

func TestSyncCapturesApprovedOrder(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Petty cash", "total_budget": 100, "auto_approve_under": 60}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingPayment)
	orderID := out["payments"].([]any)[0].(map[string]any)["id"].(string)
	// buyer approves at PayPal but never comes back to the return URL
	r, _ := e.cli.PostForm(e.ts.URL+"/mock/paypal/approve", url.Values{"token": {orderID}, "action": {"approve"}})
	r.Body.Close()
	var pay map[string]any
	if code := e.post("/api/payments/"+orderID+"/sync", nil, &pay); code != 200 {
		t.Fatalf("sync: %d %v", code, pay)
	}
	if pay["status"] != "COMPLETED" || str(pay, "capture_id") == "" {
		t.Fatalf("sync did not capture: %v", pay)
	}
	e.waitMission(ms["id"].(string), store.MissionPaid)
	if code := e.post("/api/payments/nope/sync", nil, &pay); code != 404 {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestWebhookCapturesApprovedOrder(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Petty cash", "total_budget": 100, "auto_approve_under": 60}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingPayment)
	orderID := out["payments"].([]any)[0].(map[string]any)["id"].(string)
	r, _ := e.cli.PostForm(e.ts.URL+"/mock/paypal/approve", url.Values{"token": {orderID}, "action": {"approve"}})
	r.Body.Close()

	event := fmt.Sprintf(`{"id":"WH-1","event_type":"CHECKOUT.ORDER.APPROVED","resource_type":"checkout-order","resource":{"id":%q,"status":"APPROVED"}}`, orderID)
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/paypal/webhook", strings.NewReader(event))
	req.Header.Set("Content-Type", "application/json")
	// unsigned delivery is rejected
	resp, err := e.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unsigned webhook: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, e.ts.URL+"/paypal/webhook", strings.NewReader(event))
	req.Header.Set("Paypal-Transmission-Sig", "mock")
	resp, err = e.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("signed webhook: %d", resp.StatusCode)
	}
	out = e.waitMission(ms["id"].(string), store.MissionPaid)
	pay := out["payments"].([]any)[0].(map[string]any)
	if pay["status"] != "COMPLETED" || str(pay, "capture_id") == "" {
		t.Fatalf("webhook did not capture: %v", pay)
	}
	// a second delivery (PayPal retries) must not double-count the spend
	capEvent := fmt.Sprintf(`{"id":"WH-2","event_type":"PAYMENT.CAPTURE.COMPLETED","resource_type":"capture","resource":{"id":"CAPX","status":"COMPLETED","supplementary_data":{"related_ids":{"order_id":%q}}}}`, orderID)
	req, _ = http.NewRequest(http.MethodPost, e.ts.URL+"/paypal/webhook", strings.NewReader(capEvent))
	req.Header.Set("Paypal-Transmission-Sig", "mock")
	resp, _ = e.cli.Do(req)
	resp.Body.Close()
	var mdv map[string]any
	e.get("/api/mandates/"+md["id"].(string), &mdv)
	if spent := mdv["mandate"].(map[string]any)["spent"].(float64); spent != pay["amount"].(float64) {
		t.Fatalf("spend double-counted: %v", spent)
	}
	// unknown orders are acknowledged, not retried
	req, _ = http.NewRequest(http.MethodPost, e.ts.URL+"/paypal/webhook", strings.NewReader(`{"id":"WH-3","event_type":"CHECKOUT.ORDER.APPROVED","resource_type":"checkout-order","resource":{"id":"NOPE"}}`))
	req.Header.Set("Paypal-Transmission-Sig", "mock")
	resp, _ = e.cli.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("unknown order webhook: %d", resp.StatusCode)
	}
}

func TestMissionRateLimit(t *testing.T) {
	st, _ := store.Open("")
	cat := catalog.NewMock()
	shopper := agent.NewShopper(agent.Fake{}, cat, st)
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	defer ts.Close()
	srv := New(Config{PublicURL: ts.URL, StaticDir: "/nonexistent", MaxMissionsPerHour: 2}, st, paypal.NewMock(ts.URL), shopper, cat)
	ts.Config.Handler = srv.Handler()
	e := &env{t: t, ts: ts, st: st, cli: &http.Client{}}
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "x", "total_budget": 100}, &md)
	var out map[string]any
	for i := 0; i < 2; i++ {
		if code := e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress"}, &out); code != 201 {
			t.Fatalf("mission %d: %d %v", i, code, out)
		}
	}
	if code := e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress"}, &out); code != 429 {
		t.Fatalf("expected 429, got %d", code)
	}
}

func TestCancelIsIdempotentAndNeverNegative(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	if _, reserved := e.mandateNumbers(mdID); reserved <= 0 {
		t.Fatal("expected a reservation after order creation")
	}
	for i := 0; i < 3; i++ { // the buyer reloads the cancel page
		r, _ := e.cli.Get(fmt.Sprintf("%s/paypal/cancel?token=%s", e.ts.URL, orderID))
		r.Body.Close()
	}
	e.waitMission(msID, store.MissionCancelled)
	if spent, reserved := e.mandateNumbers(mdID); spent != 0 || reserved != 0 {
		t.Fatalf("after cancel: spent=%v reserved=%v", spent, reserved)
	}
	// cancel after completion must not touch anything
	_, _, order2 := func() (string, string, string) { return e.startAutoApproved() }()
	r, _ := e.cli.PostForm(e.ts.URL+"/mock/paypal/approve", url.Values{"token": {order2}, "action": {"approve"}})
	r.Body.Close()
	r, _ = e.cli.Get(fmt.Sprintf("%s/paypal/return?token=%s", e.ts.URL, order2))
	r.Body.Close()
	r, _ = e.cli.Get(fmt.Sprintf("%s/paypal/cancel?token=%s", e.ts.URL, order2))
	r.Body.Close()
	pay, _ := e.st.GetPayment(order2)
	if pay.Status != "COMPLETED" || pay.Reserved {
		t.Fatalf("cancel after completion changed the payment: %+v", pay)
	}
}

func TestCancelThenLateCaptureKeepsBooksStraight(t *testing.T) {
	e := newEnv(t)
	mdID, _, orderID := e.startAutoApproved()
	r, _ := e.cli.Get(fmt.Sprintf("%s/paypal/cancel?token=%s", e.ts.URL, orderID))
	r.Body.Close()
	// the buyer presses Back on PayPal and approves the still-valid order
	r, _ = e.cli.PostForm(e.ts.URL+"/mock/paypal/approve", url.Values{"token": {orderID}, "action": {"approve"}})
	r.Body.Close()
	r, _ = e.cli.Get(fmt.Sprintf("%s/paypal/return?token=%s", e.ts.URL, orderID))
	r.Body.Close()
	// A cancelled order is never captured: the approver saw "cancelled" and
	// the agent may already be working on a replacement.
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentCancelled || spent != 0 || reserved != 0 || pay.CaptureID != "" {
		t.Fatalf("late return after cancel: status=%s spent=%v reserved=%v capture=%q", pay.Status, spent, reserved, pay.CaptureID)
	}
}

func TestWithdrawnOrderIsNeverCaptured(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	mdID := md["id"].(string)
	msID, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	orderID := ap["payment"].(map[string]any)["id"].(string)
	var rj map[string]any
	if code := e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": "changed my mind"}, &rj); code != 200 {
		t.Fatalf("withdraw: %d %v", code, rj)
	}
	// the buyer still approves on the (stale) PayPal tab and lands on /paypal/return
	r, _ := e.cli.PostForm(e.ts.URL+"/mock/paypal/approve", url.Values{"token": {orderID}, "action": {"approve"}})
	r.Body.Close()
	r, _ = e.cli.Get(fmt.Sprintf("%s/paypal/return?token=%s", e.ts.URL, orderID))
	r.Body.Close()
	if !strings.Contains(r.Header.Get("Location"), "payment=cancelled") {
		t.Fatalf("return should report cancelled, got %s", r.Header.Get("Location"))
	}
	// and PayPal also sends CHECKOUT.ORDER.APPROVED
	body := `{"id":"WH-9","event_type":"CHECKOUT.ORDER.APPROVED","resource_type":"checkout-order","resource":{"id":"` + orderID + `","status":"APPROVED"}}`
	req, _ := http.NewRequest("POST", e.ts.URL+"/paypal/webhook", strings.NewReader(body))
	req.Header.Set("Paypal-Transmission-Sig", "mock")
	resp, err := e.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	e.waitMission(msID, store.MissionAwaitingApproval) // the agent revised; no new order without auto-approval
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentCancelled || pay.CaptureID != "" || spent != 0 || reserved != 0 {
		t.Fatalf("withdrawn order captured: %+v spent=%v reserved=%v", pay, spent, reserved)
	}
	if o, _ := e.pp.GetOrder(context.Background(), orderID); o.Status == "COMPLETED" {
		t.Fatal("PayPal order must not have been captured")
	}
}

func TestPartialRefundCreditsOnlyTheRefundedAmount(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	out := e.waitMission(msID, store.MissionAwaitingPayment)
	e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string))
	e.waitMission(msID, store.MissionPaid)
	spent0, _ := e.mandateNumbers(mdID)
	send := func(amount string) {
		body := `{"id":"WH-r","event_type":"PAYMENT.CAPTURE.REFUNDED","resource_type":"refund","resource":{"id":"R1","status":"COMPLETED","amount":{"currency_code":"USD","value":"` + amount + `"},"supplementary_data":{"related_ids":{"order_id":"` + orderID + `"}}}}`
		req, _ := http.NewRequest("POST", e.ts.URL+"/paypal/webhook", strings.NewReader(body))
		req.Header.Set("Paypal-Transmission-Sig", "mock")
		resp, err := e.cli.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	send("10.00")
	pay, _ := e.st.GetPayment(orderID)
	spent1, _ := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentPartiallyRefunded || pay.Refunded != 10 || mandate.Round(spent0-spent1) != 10 {
		t.Fatalf("partial refund: %+v spent %v -> %v", pay, spent0, spent1)
	}
	out = e.waitMission(msID, store.MissionPaid) // still paid
	if out["proposals"].([]any)[0].(map[string]any)["status"] != store.ProposalPaid {
		t.Fatal("partial refund must not mark the proposal refunded")
	}
	// the same delivery again must not credit twice
	send("10.00")
	pay, _ = e.st.GetPayment(orderID)
	if spentDup, _ := e.mandateNumbers(mdID); pay.Refunded != 10 || spentDup != spent1 {
		t.Fatalf("redelivered refund credited twice: refunded=%v spent=%v", pay.Refunded, spentDup)
	}
	// a different refund for more than the remainder credits the rest and no more
	body2 := `{"id":"WH-r2","event_type":"PAYMENT.CAPTURE.REFUNDED","resource_type":"refund","resource":{"id":"R2","status":"COMPLETED","amount":{"currency_code":"USD","value":"1000.00"},"supplementary_data":{"related_ids":{"order_id":"` + orderID + `"}}}}`
	req2, _ := http.NewRequest("POST", e.ts.URL+"/paypal/webhook", strings.NewReader(body2))
	req2.Header.Set("Paypal-Transmission-Sig", "mock")
	resp2, err := e.cli.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	pay, _ = e.st.GetPayment(orderID)
	spent2, _ := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentRefunded || pay.Refunded != pay.Amount || spent2 != 0 {
		t.Fatalf("full refund: %+v spent=%v", pay, spent2)
	}
	e.waitMission(msID, store.MissionRefunded)
}

func TestDeniedCaptureBeforeCompletionReleasesReservation(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	body := `{"id":"WH-d","event_type":"PAYMENT.CAPTURE.DENIED","resource_type":"capture","resource":{"id":"C1","status":"DENIED","amount":{"currency_code":"USD","value":"39.95"},"supplementary_data":{"related_ids":{"order_id":"` + orderID + `"}}}}`
	req, _ := http.NewRequest("POST", e.ts.URL+"/paypal/webhook", strings.NewReader(body))
	req.Header.Set("Paypal-Transmission-Sig", "mock")
	resp, err := e.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	pay, _ := e.st.GetPayment(orderID)
	_, reserved := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentDenied || pay.Reserved || reserved != 0 {
		t.Fatalf("denied before capture: %+v reserved=%v", pay, reserved)
	}
	// the human can approve again and pay through a fresh checkout
	e.waitMission(msID, store.MissionAwaitingApproval)
	pr, _ := e.st.GetProposal(pay.ProposalID)
	if pr.Status != store.ProposalPending || pr.OrderID != "" {
		t.Fatalf("proposal not handed back: %+v", pr)
	}
}

func TestSyncVoidedOrderReleasesReservation(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	e.pp.SetStatus(orderID, "VOIDED")
	var pay map[string]any
	if code := e.post("/api/payments/"+orderID+"/sync", nil, &pay); code != 200 || pay["status"] != "VOIDED" {
		t.Fatalf("sync: %d %v", code, pay)
	}
	e.waitMission(msID, store.MissionCancelled)
	if _, reserved := e.mandateNumbers(mdID); reserved != 0 {
		t.Fatalf("reservation not released: %v", reserved)
	}
}

func TestRefundWebhookReturnsSpend(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	r, _ := e.cli.PostForm(e.ts.URL+"/mock/paypal/approve", url.Values{"token": {orderID}, "action": {"approve"}})
	r.Body.Close()
	r, _ = e.cli.Get(fmt.Sprintf("%s/paypal/return?token=%s", e.ts.URL, orderID))
	r.Body.Close()
	e.waitMission(msID, store.MissionPaid)
	if spent, _ := e.mandateNumbers(mdID); spent <= 0 {
		t.Fatal("expected spend after capture")
	}
	ev := fmt.Sprintf(`{"id":"WH-R","event_type":"PAYMENT.CAPTURE.REFUNDED","resource_type":"refund","resource":{"id":"REF1","status":"COMPLETED","supplementary_data":{"related_ids":{"order_id":%q}}}}`, orderID)
	for i := 0; i < 2; i++ { // PayPal retries deliveries
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/paypal/webhook", strings.NewReader(ev))
		req.Header.Set("Paypal-Transmission-Sig", "mock")
		resp, _ := e.cli.Do(req)
		resp.Body.Close()
	}
	e.waitMission(msID, store.MissionRefunded)
	if spent, reserved := e.mandateNumbers(mdID); spent != 0 || reserved != 0 {
		t.Fatalf("after refund: spent=%v reserved=%v", spent, reserved)
	}
	pay, _ := e.st.GetPayment(orderID)
	if pay.Status != "REFUNDED" {
		t.Fatalf("payment status = %s", pay.Status)
	}
}

func TestRejectApprovedUnpaidWithdrawsOrder(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	out := e.waitMission(msID, store.MissionAwaitingPayment)
	pid := out["proposals"].([]any)[0].(map[string]any)["id"].(string)
	var rj map[string]any
	if code := e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": "changed my mind, pick a cheaper one"}, &rj); code != 200 {
		t.Fatalf("reject approved-unpaid: %d %v", code, rj)
	}
	pay, _ := e.st.GetPayment(orderID)
	if pay.Status != "CANCELLED" || pay.Reserved {
		t.Fatalf("order not withdrawn: %+v", pay)
	}
	// The agent proposes again at once and, under the threshold, a new order
	// appears; the mandate must then hold exactly that order's reservation,
	// which proves the withdrawn order's reservation was released.
	out = e.waitMission(msID, store.MissionAwaitingPayment)
	if len(out["proposals"].([]any)) != 2 {
		t.Fatalf("expected a second proposal, got %d", len(out["proposals"].([]any)))
	}
	ids := out["payments"].([]any)
	if len(ids) != 2 {
		t.Fatalf("expected two orders, got %d", len(ids))
	}
	newOrder := ids[0].(map[string]any)["id"].(string)
	if newOrder == orderID {
		newOrder = ids[1].(map[string]any)["id"].(string)
	}
	newPay, _ := e.st.GetPayment(newOrder)
	if _, reserved := e.mandateNumbers(mdID); reserved != newPay.Amount {
		t.Fatalf("reserved %v, want exactly the new order's %v", reserved, newPay.Amount)
	}
	// a paid proposal cannot be rejected
	r, _ := e.cli.PostForm(e.ts.URL+"/mock/paypal/approve", url.Values{"token": {newOrder}, "action": {"approve"}})
	r.Body.Close()
	r, _ = e.cli.Get(fmt.Sprintf("%s/paypal/return?token=%s", e.ts.URL, newOrder))
	r.Body.Close()
	e.waitMission(msID, store.MissionPaid)
	pid2 := out["proposals"].([]any)[1].(map[string]any)["id"].(string)
	if code := e.post("/api/proposals/"+pid2+"/reject", map[string]any{"reason": "x"}, &rj); code != 409 {
		t.Fatalf("expected 409 rejecting a paid proposal, got %d", code)
	}
}

func TestConcurrentApprovalsReserveOnce(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Desk", "total_budget": 300}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingApproval)
	pid := out["proposals"].([]any)[0].(map[string]any)["id"].(string)
	done := make(chan int, 5)
	for i := 0; i < 5; i++ {
		go func() {
			var ap map[string]any
			done <- e.post("/api/proposals/"+pid+"/approve", nil, &ap)
		}()
	}
	for i := 0; i < 5; i++ {
		if code := <-done; code != 200 {
			t.Fatalf("approve returned %d", code)
		}
	}
	pays := e.st.ListPayments()
	if len(pays) != 1 {
		t.Fatalf("expected exactly one order, got %d", len(pays))
	}
	if _, reserved := e.mandateNumbers(md["id"].(string)); reserved != pays[0].Amount {
		t.Fatalf("reserved %v, want %v", reserved, pays[0].Amount)
	}
}

// payThroughMockCheckout plays the buyer: approve on the simulator page and
// follow the redirect back to /paypal/return. It returns where the app sent
// the buyer next.
func (e *env) payThroughMockCheckout(approveURL string) string {
	e.t.Helper()
	token := approveURL[strings.LastIndex(approveURL, "=")+1:]
	r, err := e.cli.PostForm(e.ts.URL+"/mock/paypal/approve", url.Values{"token": {token}, "action": {"approve"}})
	if err != nil {
		e.t.Fatal(err)
	}
	r.Body.Close()
	r2, err := e.cli.Get(r.Header.Get("Location"))
	if err != nil {
		e.t.Fatal(err)
	}
	r2.Body.Close()
	return r2.Header.Get("Location")
}

// mandateVault reads the saved token id from the store: the API never
// returns it (see TestVaultTokenIsNeverExposed).
func (e *env) mandateVault(id string) string {
	md, err := e.st.GetMandate(id)
	if err != nil {
		return ""
	}
	return md.VaultID
}

func (e *env) pendingProposal(mandateID string) (missionID, proposalID string) {
	e.t.Helper()
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": mandateID, "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingApproval)
	return ms["id"].(string), out["proposals"].([]any)[0].(map[string]any)["id"].(string)
}

func TestSavedPayPalPaysApprovedCartsWithoutCheckout(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 400, "save_payment_method": true, "vault_id": "client-cannot-set-this"}, &md)
	mdID := md["id"].(string)
	if e.mandateVault(mdID) != "" {
		t.Fatal("vault id must not be settable through the API")
	}

	// First purchase goes through checkout and saves the account.
	mid, pid := e.pendingProposal(mdID)
	var ap map[string]any
	if code := e.post("/api/proposals/"+pid+"/approve", nil, &ap); code != 200 {
		t.Fatalf("approve: %d %v", code, ap)
	}
	if ap["approve_url"] == "" {
		t.Fatal("first purchase must go through checkout")
	}
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(mid, store.MissionPaid)
	vault := e.mandateVault(mdID)
	if vault == "" || !e.pp.Vaulted(vault) {
		t.Fatalf("account not saved on the mandate (vault %q)", vault)
	}
	spent1, _ := e.mandateNumbers(mdID)

	// Second purchase: one click, no checkout, paid at once.
	mid2, pid2 := e.pendingProposal(mdID)
	if code := e.post("/api/proposals/"+pid2+"/approve", nil, &ap); code != 200 {
		t.Fatalf("approve 2: %d %v", code, ap)
	}
	if ap["approve_url"] != "" {
		t.Fatalf("expected no checkout, got approve_url %v", ap["approve_url"])
	}
	pay := ap["payment"].(map[string]any)
	if pay["status"] != "COMPLETED" || pay["vaulted"] != true || str(pay, "capture_id") == "" || str(pay, "payer_email") == "" {
		t.Fatalf("payment = %v", pay)
	}
	out := e.waitMission(mid2, store.MissionPaid)
	if out["proposals"].([]any)[0].(map[string]any)["status"] != store.ProposalPaid {
		t.Fatalf("proposal not paid: %v", out["proposals"])
	}
	spent2, reserved := e.mandateNumbers(mdID)
	if reserved != 0 || spent2 <= spent1 {
		t.Fatalf("accounting: spent %v -> %v, reserved %v", spent1, spent2, reserved)
	}
	if e.mandateVault(mdID) != vault {
		t.Fatal("vault id changed")
	}
	// approving the paid proposal again returns the same order and charges nothing
	if code := e.post("/api/proposals/"+pid2+"/approve", nil, &ap); code != 200 {
		t.Fatalf("re-approve: %d", code)
	}
	if spent, _ := e.mandateNumbers(mdID); spent != 79.9 {
		t.Fatalf("re-approval charged again: spent %v", spent)
	}
	var ledger []map[string]any
	e.get("/api/ledger", &ledger)
	if len(ledger) != 2 {
		t.Fatalf("ledger rows = %d", len(ledger))
	}
}

func TestSavedPayPalAutoApprovePaysDuringTheRun(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Petty cash", "total_budget": 200, "auto_approve_under": 60, "save_payment_method": true}, &md)
	mdID := md["id"].(string)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": mdID, "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingPayment)
	e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string))
	e.waitMission(ms["id"].(string), store.MissionPaid)
	if e.mandateVault(mdID) == "" {
		t.Fatal("account not saved")
	}

	// The next under-threshold cart is paid while the agent is still running.
	var ms2 map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": mdID, "goal": "aeropress coffee maker"}, &ms2)
	out = e.waitMission(ms2["id"].(string), store.MissionPaid)
	pay := out["payments"].([]any)[0].(map[string]any)
	if pay["vaulted"] != true || pay["status"] != "COMPLETED" {
		t.Fatalf("payment = %v", pay)
	}
	if u, _ := pay["approve_url"].(string); u != "" {
		t.Fatalf("no checkout expected, got %s", u)
	}
	var sawPaid bool
	for _, ev := range out["events"].([]any) {
		if ev.(map[string]any)["type"] == "paid" {
			sawPaid = true
		}
	}
	if !sawPaid {
		t.Fatal("paid event missing from the mission log")
	}
	spent, reserved := e.mandateNumbers(mdID)
	if reserved != 0 || spent <= 0 {
		t.Fatalf("accounting: spent %v reserved %v", spent, reserved)
	}
}

func TestSavedPayPalFallsBackToCheckoutWhenTokenIsGone(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 400, "save_payment_method": true}, &md)
	mdID := md["id"].(string)
	mid, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(mid, store.MissionPaid)
	vault := e.mandateVault(mdID)
	e.pp.Revoke(vault) // the buyer removed the permission in their PayPal account

	mid2, pid2 := e.pendingProposal(mdID)
	if code := e.post("/api/proposals/"+pid2+"/approve", nil, &ap); code != 200 {
		t.Fatalf("approve: %d %v", code, ap)
	}
	if ap["approve_url"] == "" {
		t.Fatalf("expected a checkout fallback, got %v", ap)
	}
	if e.mandateVault(mdID) != "" {
		t.Fatal("dead token should have been forgotten")
	}
	_, reserved := e.mandateNumbers(mdID)
	if reserved <= 0 {
		t.Fatal("fallback order should reserve the budget")
	}
	// paying the fallback order saves a fresh account
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(mid2, store.MissionPaid)
	if v := e.mandateVault(mdID); v == "" || v == vault {
		t.Fatalf("expected a new saved account, got %q", v)
	}
}

func TestInsightsAggregateCapturedSpend(t *testing.T) {
	e := newEnv(t)
	_, mid, orderID := e.startAutoApproved()
	var ins map[string]any
	e.get("/api/insights", &ins)
	if ins["totals"].(map[string]any)["captured"].(float64) != 0 || len(ins["items"].([]any)) == 0 {
		t.Fatalf("pending order must be listed but not counted: %v", ins["totals"])
	}
	var pay map[string]any
	e.get("/api/missions/"+mid, &pay)
	approveURL := pay["payments"].([]any)[0].(map[string]any)["approve_url"].(string)
	e.payThroughMockCheckout(approveURL)
	e.waitMission(mid, store.MissionPaid)
	e.get("/api/insights", &ins)
	tot := ins["totals"].(map[string]any)
	if tot["captured"].(float64) <= 0 || tot["orders"].(float64) != 1 || tot["items"].(float64) < 1 {
		t.Fatalf("totals = %v", tot)
	}
	cats := ins["by_category"].([]any)
	if len(cats) == 0 || cats[0].(map[string]any)["key"] != "kitchen" {
		t.Fatalf("by_category = %v", cats)
	}
	if len(ins["by_merchant"].([]any)) == 0 || len(ins["by_day"].([]any)) != 1 {
		t.Fatalf("buckets = %v %v", ins["by_merchant"], ins["by_day"])
	}
	items := ins["items"].([]any)
	first := items[0].(map[string]any)
	if first["order_id"] != orderID || first["status"] != "COMPLETED" || first["line_total"].(float64) <= 0 || first["category"] != "kitchen" {
		t.Fatalf("item = %v", first)
	}
	md := ins["mandates"].([]any)[0].(map[string]any)
	if md["orders"].(float64) != 1 || md["spent"].(float64) <= 0 {
		t.Fatalf("mandate row = %v", md)
	}
}

func TestApplySuggestionAmendsMandateAndRetries(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Tight", "total_budget": 300, "max_per_item": 300, "allowed_categories": []string{"computers"}}, &md)
	mdID := md["id"].(string)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": mdID, "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionNoProposal)
	m := out["mission"].(map[string]any)
	sug, _ := m["suggestion"].(map[string]any)
	if sug == nil || sug["field"] != "allowed_categories" {
		t.Fatalf("expected a category suggestion, got %v", m["suggestion"])
	}
	var applied map[string]any
	if code := e.post("/api/missions/"+ms["id"].(string)+"/apply-suggestion", nil, &applied); code != 201 {
		t.Fatalf("apply: %d %v", code, applied)
	}
	retryID := applied["mission"].(map[string]any)["id"].(string)
	var mdv map[string]any
	e.get("/api/mandates/"+mdID, &mdv)
	cats := mdv["mandate"].(map[string]any)["allowed_categories"].([]any)
	if len(cats) != 2 || cats[1] != "kitchen" {
		t.Fatalf("mandate not amended: %v", cats)
	}
	out = e.waitMission(retryID, store.MissionAwaitingApproval)
	if out["mission"].(map[string]any)["retry_of"] != ms["id"] {
		t.Fatalf("retry link missing: %v", out["mission"])
	}
	if code := e.post("/api/missions/"+ms["id"].(string)+"/apply-suggestion", nil, &applied); code != 409 {
		t.Fatalf("second apply should be refused, got %d", code)
	}
}

func TestStandingOrderRunsNowAndOnSchedule(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 500, "auto_approve_under": 0}, &md)
	var ms map[string]any
	if code := e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress coffee maker", "repeat_days": 30}, &ms); code != 201 {
		t.Fatalf("create standing order: %d %v", code, ms)
	}
	id := ms["id"].(string)
	if ms["repeat_days"].(float64) != 30 || ms["next_run_at"] == nil {
		t.Fatalf("standing order fields missing: %v", ms)
	}
	e.waitMission(id, store.MissionAwaitingApproval) // the first run is the mission itself

	var run map[string]any
	if code := e.post("/api/missions/"+id+"/run-now", nil, &run); code != 201 {
		t.Fatalf("run now: %d %v", code, run)
	}
	if run["repeat_of"] != id {
		t.Fatalf("run not linked: %v", run)
	}
	e.waitMission(run["id"].(string), store.MissionAwaitingApproval)
	var parent map[string]any
	e.get("/api/missions/"+id, &parent)
	if parent["mission"].(map[string]any)["last_run_id"] != run["id"] {
		t.Fatalf("last run not recorded: %v", parent["mission"])
	}

	// the scheduler starts due runs and reschedules
	mBefore, _ := e.st.GetMission(id)
	if n := e.srv.runDueRepeats(time.Now()); n != 0 {
		t.Fatalf("nothing should be due yet, started %d", n)
	}
	if n := e.srv.runDueRepeats(mBefore.NextRunAt.Add(time.Minute)); n != 1 {
		t.Fatalf("expected one due run, started %d", n)
	}
	mAfter, _ := e.st.GetMission(id)
	if !mAfter.NextRunAt.After(*mBefore.NextRunAt) {
		t.Fatal("next run was not rescheduled")
	}
	var runs int
	for _, m := range e.st.ListMissions("") {
		if m.RepeatOf == id {
			runs++
		}
	}
	if runs != 2 {
		t.Fatalf("runs = %d, want 2", runs)
	}

	// stopping ends the schedule
	if code := e.post("/api/missions/"+id+"/stop-repeat", nil, &parent); code != 200 || parent["repeat_days"] != nil {
		t.Fatalf("stop: %d %v", code, parent)
	}
	if n := e.srv.runDueRepeats(time.Now().Add(400 * 24 * time.Hour)); n != 0 {
		t.Fatalf("stopped order still ran %d time(s)", n)
	}
	if code := e.post("/api/missions/"+id+"/run-now", nil, &run); code != 409 {
		t.Fatalf("run-now after stop: %d", code)
	}
}

func TestForgetSavedPaymentRevokesOneClick(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 400, "save_payment_method": true}, &md)
	mdID := md["id"].(string)
	mid, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(mid, store.MissionPaid)
	if e.mandateVault(mdID) == "" {
		t.Fatal("account not saved")
	}
	var out map[string]any
	if code := e.post("/api/mandates/"+mdID+"/forget-payment", nil, &out); code != 200 || out["vault_id"] != nil {
		t.Fatalf("forget: %d %v", code, out)
	}
	if code := e.post("/api/mandates/"+mdID+"/forget-payment", nil, &out); code != 409 {
		t.Fatalf("second forget: %d", code)
	}
	_, pid2 := e.pendingProposal(mdID)
	e.post("/api/proposals/"+pid2+"/approve", nil, &ap)
	if ap["approve_url"] == "" {
		t.Fatal("expected a checkout after forgetting the saved account")
	}
}

func TestNegotiatedServiceIsApprovedAndPaidLikeAnyCart(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Freelance", "total_budget": 600, "max_per_item": 200}, &md)
	var ms map[string]any
	if code := e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "a two-hour product photo shoot", "kind": "negotiate"}, &ms); code != 201 {
		t.Fatalf("create: %d %v", code, ms)
	}
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingApproval)
	m := out["mission"].(map[string]any)
	neg := m["negotiation"].(map[string]any)
	if m["kind"] != "negotiate" || neg["outcome"] != "agreed" || neg["agreed_price"].(float64) > 200 {
		t.Fatalf("mission = %v", m)
	}
	pid := out["proposals"].([]any)[0].(map[string]any)["id"].(string)
	var ap map[string]any
	if code := e.post("/api/proposals/"+pid+"/approve", nil, &ap); code != 200 {
		t.Fatalf("approve: %d %v", code, ap)
	}
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(ms["id"].(string), store.MissionPaid)
	var ledger []map[string]any
	e.get("/api/ledger", &ledger)
	if len(ledger) != 1 || ledger[0]["amount"].(float64) != neg["agreed_price"].(float64) {
		t.Fatalf("ledger = %v", ledger)
	}
	if code := e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "x", "kind": "auction"}, &ms); code != 400 {
		t.Fatalf("bad kind: %d", code)
	}
}

func TestVaultOrderCompletedAtCreationIsAccountedOnce(t *testing.T) {
	e := newEnv(t)
	e.pp.VaultCompletesOnCreate = true
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Petty cash", "total_budget": 200, "auto_approve_under": 60, "save_payment_method": true}, &md)
	mdID := md["id"].(string)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": mdID, "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingPayment)
	e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string))
	e.waitMission(ms["id"].(string), store.MissionPaid)
	spent1, _ := e.mandateNumbers(mdID)

	// the second cart is paid with the saved account and PayPal completes it at creation
	var ms2 map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": mdID, "goal": "aeropress coffee maker"}, &ms2)
	out = e.waitMission(ms2["id"].(string), store.MissionPaid)
	pay := out["payments"].([]any)[0].(map[string]any)
	if pay["status"] != "COMPLETED" || pay["vaulted"] != true || str(pay, "capture_id") == "" {
		t.Fatalf("payment = %v", pay)
	}
	if out["proposals"].([]any)[0].(map[string]any)["status"] != store.ProposalPaid {
		t.Fatalf("proposal not paid: %v", out["proposals"])
	}
	spent2, reserved := e.mandateNumbers(mdID)
	if reserved != 0 || spent2 <= spent1 {
		t.Fatalf("accounting: spent %v -> %v reserved %v", spent1, spent2, reserved)
	}
	var paidEvents int
	for _, ev := range out["events"].([]any) {
		if ev.(map[string]any)["type"] == "paid" {
			paidEvents++
		}
	}
	if paidEvents != 1 {
		t.Fatalf("paid events = %d", paidEvents)
	}
	// syncing a completed payment never double-counts
	var synced map[string]any
	e.post("/api/payments/"+pay["id"].(string)+"/sync", nil, &synced)
	spent3, reserved3 := e.mandateNumbers(mdID)
	if spent3 != spent2 || reserved3 != 0 {
		t.Fatalf("sync changed the books: %v %v", spent3, reserved3)
	}
}

func TestSyncRepairsACompletedButUnaccountedPayment(t *testing.T) {
	// Simulates the bug class: the payment row says COMPLETED but the
	// proposal, mission and reservation were never finished.
	e := newEnv(t)
	mdID, mid, orderID := e.startAutoApproved()
	_, _ = e.pp.Approve(orderID, true)
	if _, err := e.pp.CaptureOrder(context.Background(), orderID); err != nil {
		t.Fatal(err)
	}
	_, _ = e.st.UpdatePayment(orderID, func(p *store.Payment) { p.Status = "COMPLETED" })
	var synced map[string]any
	if code := e.post("/api/payments/"+orderID+"/sync", nil, &synced); code != 200 {
		t.Fatalf("sync: %d %v", code, synced)
	}
	out := e.waitMission(mid, store.MissionPaid)
	if out["proposals"].([]any)[0].(map[string]any)["status"] != store.ProposalPaid {
		t.Fatalf("proposal = %v", out["proposals"])
	}
	spent, reserved := e.mandateNumbers(mdID)
	if reserved != 0 || spent <= 0 {
		t.Fatalf("accounting: spent %v reserved %v", spent, reserved)
	}
}

func TestLateCaptureOfASupersededOrderIsBookedButNotMistakenForTheCurrentOne(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 400}, &md)
	mdID := md["id"].(string)
	mid, pid := e.pendingProposal(mdID)
	var ap map[string]any
	if code := e.post("/api/proposals/"+pid+"/approve", nil, &ap); code != 200 {
		t.Fatalf("approve: %d %v", code, ap)
	}
	o2 := ap["payment"].(map[string]any)["id"].(string)
	// an older order for the same proposal (a capture that failed on our side) that PayPal captures late
	e.st.PutPayment(&store.Payment{ID: "MOCK-OLD", ProposalID: pid, MissionID: mid, MandateID: mdID, Status: store.PaymentCaptureFailed, Amount: 39.95, Currency: "USD", Mode: "mock", CreatedAt: time.Now()})
	spentBefore, reservedBefore := e.mandateNumbers(mdID)
	e.srv.markPaid("MOCK-OLD", &paypal.Order{ID: "MOCK-OLD", Status: "COMPLETED", CaptureID: "CAPOLD", Amount: 39.95, Currency: "USD"})
	spentMid, reservedMid := e.mandateNumbers(mdID)
	if mandate.Round(spentMid-spentBefore) != 39.95 || reservedMid != reservedBefore {
		t.Fatalf("late capture must be booked without touching the live reservation: spent %v->%v reserved %v->%v", spentBefore, spentMid, reservedBefore, reservedMid)
	}
	var detail map[string]any
	e.get("/api/missions/"+mid, &detail)
	if detail["proposals"].([]any)[0].(map[string]any)["status"] != store.ProposalApproved {
		t.Fatalf("stale capture must not mark the proposal paid: %v", detail["proposals"])
	}
	// the current order completes normally and is booked too
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(mid, store.MissionPaid)
	spentEnd, reservedEnd := e.mandateNumbers(mdID)
	if reservedEnd != 0 || mandate.Round(spentEnd-spentMid) <= 0 {
		t.Fatalf("current order not booked: spent %v->%v reserved %v", spentMid, spentEnd, reservedEnd)
	}
	p2, _ := e.st.GetPayment(o2)
	p1, _ := e.st.GetPayment("MOCK-OLD")
	if !p1.Accounted || !p2.Accounted {
		t.Fatalf("both captures must be accounted: old=%v new=%v", p1.Accounted, p2.Accounted)
	}
	// booking is idempotent per payment
	e.srv.markPaid("MOCK-OLD", &paypal.Order{ID: "MOCK-OLD", Status: "COMPLETED", CaptureID: "CAPOLD", Amount: 39.95, Currency: "USD"})
	if spentAgain, _ := e.mandateNumbers(mdID); spentAgain != spentEnd {
		t.Fatalf("double booking: %v -> %v", spentEnd, spentAgain)
	}
}

func TestCaptureAfterWithdrawalIsBookedAndFlagged(t *testing.T) {
	// The approver withdraws the order; PayPal captures it anyway (buyer was
	// mid-checkout). The money moved, so it is booked and flagged; the
	// proposal stays rejected.
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	mdID := md["id"].(string)
	msID, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	orderID := ap["payment"].(map[string]any)["id"].(string)
	var rj map[string]any
	e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": "no"}, &rj)
	_, _ = e.pp.Approve(orderID, true)
	captured, err := e.pp.CaptureOrder(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.markPaid(orderID, captured)
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	if !pay.Accounted || pay.Status != store.PaymentCompleted || spent != pay.Amount || reserved != 0 {
		t.Fatalf("late capture not booked: %+v spent=%v reserved=%v", pay, spent, reserved)
	}
	e.waitMission(msID, store.MissionAwaitingApproval) // the agent's revised proposal is untouched
	var detail map[string]any
	e.get("/api/missions/"+msID, &detail)
	var flagged bool
	for _, ev := range detail["events"].([]any) {
		if d, ok := ev.(map[string]any)["data"].(map[string]any); ok && d["refund_required"] == true {
			flagged = true
		}
	}
	if !flagged {
		t.Fatal("capture after withdrawal must be flagged for refund")
	}
}

func TestRestartRecomputesReservations(t *testing.T) {
	e := newEnv(t)
	mdID, _, orderID := e.startAutoApproved()
	// simulate a crash that left the mandate's reserved total inflated
	_ = e.st.UpdateMandate(mdID, func(m *mandate.Mandate) error { m.Reserved = 999; return nil })
	cat := catalog.NewMock()
	shopper := agent.NewShopper(agent.Fake{}, cat, e.st)
	srv2 := New(Config{PublicURL: e.ts.URL, StaticDir: "/nonexistent", Currency: "USD"}, e.st, e.pp, shopper, cat)
	t.Cleanup(srv2.Close)
	pay, _ := e.st.GetPayment(orderID)
	_, reserved := e.mandateNumbers(mdID)
	if reserved != pay.Amount {
		t.Fatalf("reserved after restart = %v, want %v", reserved, pay.Amount)
	}
}

func TestInFlightApprovalKeepsItsReservationWhenAnotherOrderSettles(t *testing.T) {
	// Budget 100. Order X (39.95) awaits checkout; P2 (39.95) is being
	// created at PayPal while X is captured. The release for X must not
	// drop P2's in-flight reservation, and a third approval must see it.
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Tight", "total_budget": 100}, &md)
	mdID := md["id"].(string)
	m1, p1 := e.pendingProposal(mdID)
	var ap1 map[string]any
	e.post("/api/proposals/"+p1+"/approve", nil, &ap1)
	x := ap1["payment"].(map[string]any)["id"].(string)
	_, p2 := e.pendingProposal(mdID)
	var midCreate float64
	e.pp.OnCreate = func(req paypal.CreateOrderRequest) {
		if req.CustomID != p2 {
			return
		}
		e.pp.OnCreate = nil
		// X is captured while P2's order is being created
		e.payThroughMockCheckout(ap1["approve_url"].(string))
		e.waitMission(m1, store.MissionPaid)
		_, midCreate = e.mandateNumbers(mdID)
	}
	var ap2 map[string]any
	if code := e.post("/api/proposals/"+p2+"/approve", nil, &ap2); code != 200 {
		t.Fatalf("approve P2: %d %v", code, ap2)
	}
	if midCreate != 39.95 {
		t.Fatalf("in-flight reservation dropped during the capture of %s: reserved=%v", x, midCreate)
	}
	spent, reserved := e.mandateNumbers(mdID)
	if spent != 39.95 || reserved != 39.95 {
		t.Fatalf("after both: spent=%v reserved=%v", spent, reserved)
	}
	// a third proposal (39.95) no longer fits: 100 - 39.95 - 39.95 = 20.10
	_, p3 := e.pendingProposalAllowingBlock(mdID)
	if p3 != "" {
		var ap3 map[string]any
		if code := e.post("/api/proposals/"+p3+"/approve", nil, &ap3); code != 409 {
			t.Fatalf("third approval should be refused by the budget, got %d %v", code, ap3)
		}
	}
}

// pendingProposalAllowingBlock starts a mission and returns its first proposal
// id, or "" when the agent could not propose inside the remaining budget.
func (e *env) pendingProposalAllowingBlock(mandateID string) (string, string) {
	e.t.Helper()
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": mandateID, "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMissionOneOf(ms["id"].(string), store.MissionAwaitingApproval, store.MissionNoProposal)
	if out["mission"].(map[string]any)["status"] != store.MissionAwaitingApproval {
		return ms["id"].(string), ""
	}
	return ms["id"].(string), out["proposals"].([]any)[0].(map[string]any)["id"].(string)
}

func TestProposalChangedWhileOrderWasBeingCreated(t *testing.T) {
	e := newEnv(t)
	e.pp.VaultCompletesOnCreate = true
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 400, "save_payment_method": true}, &md)
	mdID := md["id"].(string)
	mid, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(mid, store.MissionPaid)
	spent1, _ := e.mandateNumbers(mdID)

	// second proposal: PayPal completes the vault order at creation, but the
	// proposal is cancelled by some other path while the call is in flight
	mid2, pid2 := e.pendingProposal(mdID)
	e.pp.OnCreate = func(req paypal.CreateOrderRequest) {
		e.pp.OnCreate = nil
		_, _ = e.st.UpdateProposal(pid2, func(p *store.Proposal) { p.Status = store.ProposalCancelled })
	}
	if code := e.post("/api/proposals/"+pid2+"/approve", nil, &ap); code != 409 {
		t.Fatalf("expected 409, got %d %v", code, ap)
	}
	var detail map[string]any
	e.get("/api/missions/"+mid2, &detail)
	pays := detail["payments"].([]any)
	if len(pays) != 1 {
		t.Fatalf("payments = %v", pays)
	}
	pay := pays[0].(map[string]any)
	if pay["accounted"] != true || pay["status"] != store.PaymentCompleted {
		t.Fatalf("captured money must be booked: %v", pay)
	}
	spent2, reserved := e.mandateNumbers(mdID)
	if mandate.Round(spent2-spent1) <= 0 || reserved != 0 {
		t.Fatalf("accounting: spent %v -> %v reserved %v", spent1, spent2, reserved)
	}
	if detail["proposals"].([]any)[0].(map[string]any)["status"] != store.ProposalCancelled {
		t.Fatalf("proposal must stay cancelled: %v", detail["proposals"])
	}
}

func TestRefundBeforeCaptureIsParkedThenApplied(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	body := `{"id":"WH-early","event_type":"PAYMENT.CAPTURE.REFUNDED","resource_type":"refund","resource":{"id":"R-early","status":"COMPLETED","amount":{"currency_code":"USD","value":"5.00"},"supplementary_data":{"related_ids":{"order_id":"` + orderID + `"}}}}`
	req, _ := http.NewRequest("POST", e.ts.URL+"/paypal/webhook", strings.NewReader(body))
	req.Header.Set("Paypal-Transmission-Sig", "mock")
	resp, err := e.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	pay, _ := e.st.GetPayment(orderID)
	if len(pay.PendingRefunds) != 1 || pay.Refunded != 0 {
		t.Fatalf("refund should be parked: %+v", pay)
	}
	var out map[string]any
	e.get("/api/missions/"+msID, &out)
	e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string))
	e.waitMission(msID, store.MissionPaid)
	pay, _ = e.st.GetPayment(orderID)
	spent, _ := e.mandateNumbers(mdID)
	if pay.Refunded != 5 || pay.Status != store.PaymentPartiallyRefunded || len(pay.PendingRefunds) != 0 || mandate.Round(pay.Amount-spent) != 5 {
		t.Fatalf("parked refund not applied: %+v spent=%v", pay, spent)
	}
}

func TestSyncBooksACaptureOfAWithdrawnOrder(t *testing.T) {
	// Same situation as above, but the capture is discovered by the
	// "check status with PayPal" button instead of a webhook.
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	mdID := md["id"].(string)
	_, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	orderID := ap["payment"].(map[string]any)["id"].(string)
	var rj map[string]any
	e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": "no"}, &rj)
	_, _ = e.pp.Approve(orderID, true)
	if _, err := e.pp.CaptureOrder(context.Background(), orderID); err != nil {
		t.Fatal(err)
	}
	var synced map[string]any
	if code := e.post("/api/payments/"+orderID+"/sync", nil, &synced); code != 200 {
		t.Fatalf("sync: %d %v", code, synced)
	}
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	if !pay.Accounted || pay.Status != store.PaymentCompleted || spent != pay.Amount || reserved != 0 {
		t.Fatalf("sync must book the capture: %+v spent=%v reserved=%v", pay, spent, reserved)
	}
	pr, _ := e.st.GetProposal(pid)
	if pr.Status != store.ProposalRejected {
		t.Fatalf("proposal must stay rejected, got %s", pr.Status)
	}
	// a second sync changes nothing
	e.post("/api/payments/"+orderID+"/sync", nil, &synced)
	if spent2, _ := e.mandateNumbers(mdID); spent2 != spent {
		t.Fatalf("second sync double-booked: %v -> %v", spent, spent2)
	}
}

func TestDeniedCaptureVoidsOnlyAnOpenOrder(t *testing.T) {
	e := newEnv(t)
	mdID, _, orderID := e.startAutoApproved()
	e.srv.markRefunded(orderID, store.PaymentDenied, 0, "")
	pay, _ := e.st.GetPayment(orderID)
	_, reserved := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentDenied || reserved != 0 || len(pay.PendingRefunds) != 0 {
		t.Fatalf("denied capture must void the order: %+v reserved=%v", pay, reserved)
	}
	e.srv.markRefunded(orderID, store.PaymentDenied, 0, "") // redelivery is a no-op
	pay2, _ := e.st.GetPayment(orderID)
	if pay2.Status != store.PaymentDenied || len(pay2.PendingRefunds) != 0 {
		t.Fatalf("redelivered denial changed the row: %+v", pay2)
	}
}

func (e *env) webhook(body string) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+"/paypal/webhook", strings.NewReader(body))
	req.Header.Set("Paypal-Transmission-Sig", "mock")
	resp, err := e.cli.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
}

func (e *env) refundWebhook(orderID, refundID, status, amount string) {
	e.t.Helper()
	e.webhook(`{"id":"WH-` + refundID + `","event_type":"PAYMENT.CAPTURE.` + status + `","resource_type":"refund","resource":{"id":"` + refundID + `","status":"COMPLETED","amount":{"currency_code":"USD","value":"` + amount + `"},"supplementary_data":{"related_ids":{"order_id":"` + orderID + `"}}}}`)
}

func TestDeclinedCaptureIsNeverBookedBySync(t *testing.T) {
	// PayPal reports the order COMPLETED after a capture attempt even when
	// the capture itself was DECLINED; only the capture status says whether
	// money moved.
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	_, _ = e.pp.Approve(orderID, true)
	e.pp.NextCaptureStatus = "DECLINED"
	if _, err := e.pp.CaptureOrder(context.Background(), orderID); err != nil {
		t.Fatal(err)
	}
	e.srv.markRefunded(orderID, store.PaymentDenied, 0, "") // the DENIED webhook voids the row
	var synced map[string]any
	for i := 0; i < 2; i++ {
		if code := e.post("/api/payments/"+orderID+"/sync", nil, &synced); code != 200 {
			t.Fatalf("sync: %d %v", code, synced)
		}
	}
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	if pay.Accounted || pay.Status != store.PaymentDenied || spent != 0 || reserved != 0 {
		t.Fatalf("declined capture must not be booked: %+v spent=%v reserved=%v", pay, spent, reserved)
	}
	ms, _ := e.st.GetMission(msID)
	if ms.Status == store.MissionPaid {
		t.Fatal("mission must not read paid")
	}
}

func TestPendingCaptureBooksOnlyWhenCompleted(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	var out map[string]any
	e.get("/api/missions/"+msID, &out)
	e.pp.NextCaptureStatus = "PENDING" // an eCheck: the buyer approved, PayPal has not settled
	e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string))
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	if pay.Accounted || pay.Status != store.PaymentPending || spent != 0 || reserved != pay.Amount {
		t.Fatalf("pending capture must stay reserved and unbooked: %+v spent=%v reserved=%v", pay, spent, reserved)
	}
	// Meanwhile nothing may capture it again or withdraw it.
	var rj map[string]any
	if code := e.post("/api/proposals/"+pay.ProposalID+"/reject", map[string]any{"reason": "no"}, &rj); code != 409 {
		t.Fatalf("withdrawing a pending capture must be refused, got %d %v", code, rj)
	}
	r, err := e.cli.Get(e.ts.URL + "/paypal/return?token=" + orderID) // the buyer reloads the return page
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if loc := r.Header.Get("Location"); !strings.HasSuffix(loc, "?payment=pending") {
		t.Fatalf("refreshed return must report pending, got %s", loc)
	}
	e.webhook(`{"id":"WH-appr","event_type":"CHECKOUT.ORDER.APPROVED","resource_type":"checkout-order","resource":{"id":"` + orderID + `","status":"APPROVED"}}`)
	e.webhook(`{"id":"WH-void","event_type":"CHECKOUT.ORDER.VOIDED","resource_type":"checkout-order","resource":{"id":"` + orderID + `","status":"VOIDED"}}`)
	if rc, err := e.cli.Get(e.ts.URL + "/paypal/cancel?token=" + orderID); err == nil { // a stale cancel link
		rc.Body.Close()
	}
	e.post("/api/payments/"+orderID+"/sync", nil, &rj)
	if cur, _ := e.st.GetPayment(orderID); cur.Status != store.PaymentPending || cur.Reserved != true {
		t.Fatalf("a pending capture must survive a reload, webhooks, a cancel link and a status check: %+v", cur)
	}
	if ms, _ := e.st.GetMission(msID); ms.Status != store.MissionAwaitingPayment {
		t.Fatalf("mission must still await the payment, got %s", ms.Status)
	}
	// PayPal completes the capture later and says so
	e.pp.SetCaptureStatus(orderID, "COMPLETED")
	e.webhook(`{"id":"WH-done","event_type":"PAYMENT.CAPTURE.COMPLETED","resource_type":"capture","resource":{"id":"` + pay.CaptureID + `","status":"COMPLETED","supplementary_data":{"related_ids":{"order_id":"` + orderID + `"}}}}`)
	e.waitMission(msID, store.MissionPaid)
	pay, _ = e.st.GetPayment(orderID)
	spent, reserved = e.mandateNumbers(mdID)
	if !pay.Accounted || pay.Status != store.PaymentCompleted || spent != pay.Amount || reserved != 0 {
		t.Fatalf("completed capture not booked: %+v spent=%v reserved=%v", pay, spent, reserved)
	}
}

func TestParkedFullRefundLeavesProposalRefunded(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	pay, _ := e.st.GetPayment(orderID)
	e.refundWebhook(orderID, "R-full", "REFUNDED", fmt.Sprintf("%.2f", pay.Amount))
	var out map[string]any
	e.get("/api/missions/"+msID, &out)
	if loc := e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string)); !strings.HasSuffix(loc, "?payment=completed") {
		t.Fatalf("the buyer must see that the capture happened, got %s", loc)
	}
	e.waitMission(msID, store.MissionRefunded)
	pay, _ = e.st.GetPayment(orderID)
	pr, _ := e.st.GetProposal(pay.ProposalID)
	spent, reserved := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentRefunded || pr.Status != store.ProposalRefunded || spent != 0 || reserved != 0 || len(pay.PendingRefunds) != 0 {
		t.Fatalf("parked full refund: pay=%+v proposal=%s spent=%v reserved=%v", pay, pr.Status, spent, reserved)
	}
}

func TestLateCaptureAlreadyRefundedIsNotFlagged(t *testing.T) {
	// Withdrawn order, captured anyway, refunded by the merchant; the refund
	// webhook lands before the capture one. No "refund required" flag.
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	mdID := md["id"].(string)
	msID, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	orderID := ap["payment"].(map[string]any)["id"].(string)
	var rj map[string]any
	e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": "no"}, &rj)
	_, _ = e.pp.Approve(orderID, true)
	captured, err := e.pp.CaptureOrder(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	e.refundWebhook(orderID, "R-late", "REFUNDED", fmt.Sprintf("%.2f", captured.Amount))
	e.srv.markPaid(orderID, captured)
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	if !pay.Accounted || pay.Status != store.PaymentRefunded || spent != 0 || reserved != 0 {
		t.Fatalf("late capture + early refund: %+v spent=%v reserved=%v", pay, spent, reserved)
	}
	var detail map[string]any
	e.get("/api/missions/"+msID, &detail)
	for _, ev := range detail["events"].([]any) {
		if d, ok := ev.(map[string]any)["data"].(map[string]any); ok && d["refund_required"] == true {
			t.Fatal("a capture that was already refunded must not be flagged")
		}
	}
	pr, _ := e.st.GetProposal(pid)
	if pr.Status != store.ProposalRejected {
		t.Fatalf("proposal must stay rejected, got %s", pr.Status)
	}
}

func TestInterruptedApprovalReusesTheOrder(t *testing.T) {
	// A crash between writing the order row and the proposal leaves a live
	// order for a pending proposal; approving again must adopt it, not
	// reserve twice.
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	mdID := md["id"].(string)
	msID, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	orderID := ap["payment"].(map[string]any)["id"].(string)
	_, _ = e.st.UpdateProposal(pid, func(p *store.Proposal) { p.Status = store.ProposalPending; p.OrderID = "" })
	_, _ = e.st.UpdateMission(msID, func(m *store.Mission) { m.Status = store.MissionAwaitingApproval; m.OrderID = "" })
	var ap2 map[string]any
	if code := e.post("/api/proposals/"+pid+"/approve", nil, &ap2); code != 200 {
		t.Fatalf("approve again: %d %v", code, ap2)
	}
	if got := ap2["payment"].(map[string]any)["id"]; got != orderID {
		t.Fatalf("a second order was created: %v (wanted %s)", got, orderID)
	}
	_, reserved := e.mandateNumbers(mdID)
	if reserved != 39.95 {
		t.Fatalf("reserved twice: %v", reserved)
	}
	pr, _ := e.st.GetProposal(pid)
	if pr.Status != store.ProposalApproved || pr.OrderID != orderID {
		t.Fatalf("proposal not repaired: %+v", pr)
	}
}

func TestRejectApprovedProposalWithoutOrder(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	_, pid := e.pendingProposal(md["id"].(string))
	_, _ = e.st.UpdateProposal(pid, func(p *store.Proposal) { p.Status = store.ProposalApproved; p.OrderID = "" })
	var rj map[string]any
	if code := e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": "changed my mind"}, &rj); code != 200 {
		t.Fatalf("reject: %d %v", code, rj)
	}
	pr, _ := e.st.GetProposal(pid)
	if pr.Status != store.ProposalRejected {
		t.Fatalf("got %s", pr.Status)
	}
}

func TestServerCloseIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.srv.Close()
	e.srv.Close()
}

func TestDeclinedCheckoutCaptureHandsTheProposalBack(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	var out map[string]any
	e.get("/api/missions/"+msID, &out)
	e.pp.NextCaptureStatus = "DECLINED"
	if loc := e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string)); !strings.HasSuffix(loc, "?payment=denied") {
		t.Fatalf("declined capture must land on the denied banner, got %s", loc)
	}
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	if pay.Accounted || pay.Status != store.PaymentDenied || spent != 0 || reserved != 0 {
		t.Fatalf("declined capture: %+v spent=%v reserved=%v", pay, spent, reserved)
	}
	pr, _ := e.st.GetProposal(pay.ProposalID)
	ms, _ := e.st.GetMission(msID)
	if pr.Status != store.ProposalPending || pr.OrderID != "" || ms.Status != store.MissionAwaitingApproval || ms.OrderID != "" {
		t.Fatalf("the human must be able to approve again: proposal=%+v mission=%s/%s", pr, ms.Status, ms.OrderID)
	}
	// approving again creates a fresh order
	var ap map[string]any
	if code := e.post("/api/proposals/"+pay.ProposalID+"/approve", nil, &ap); code != 200 {
		t.Fatalf("approve again: %d %v", code, ap)
	}
	if ap["payment"].(map[string]any)["id"] == orderID {
		t.Fatal("a declined order must not be reused")
	}
}

func TestRejectWithdrawsTheLiveOrderOfAProposalThatLostItsID(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	mdID := md["id"].(string)
	_, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	orderID := ap["payment"].(map[string]any)["id"].(string)
	_, _ = e.st.UpdateProposal(pid, func(p *store.Proposal) { p.OrderID = "" })
	var rj map[string]any
	if code := e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": "no"}, &rj); code != 200 {
		t.Fatalf("reject: %d %v", code, rj)
	}
	pay, _ := e.st.GetPayment(orderID)
	_, reserved := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentCancelled || pay.Reserved || reserved != 0 {
		t.Fatalf("orphaned order must be withdrawn: %+v reserved=%v", pay, reserved)
	}
}

func TestSyncOfAGivenUpOrderUnknownToPayPal(t *testing.T) {
	e := newEnv(t)
	mdID, _, orderID := e.startAutoApproved()
	_, _ = e.st.UpdatePayment(orderID, func(x *store.Payment) { x.Status = store.PaymentCancelled; x.Reserved = false })
	e.srv.recomputeReserved(mdID)
	e.pp.Drop(orderID) // PayPal no longer has it
	var out map[string]any
	if code := e.post("/api/payments/"+orderID+"/sync", nil, &out); code != 200 {
		t.Fatalf("a 404 at PayPal is not an error for a given-up row: %d %v", code, out)
	}
}

func TestPendingCaptureThatBouncesHandsTheProposalBack(t *testing.T) {
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	var out map[string]any
	e.get("/api/missions/"+msID, &out)
	e.pp.NextCaptureStatus = "PENDING"
	e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string))
	pay, _ := e.st.GetPayment(orderID)
	e.pp.SetCaptureStatus(orderID, "DECLINED")
	e.webhook(`{"id":"WH-denied","event_type":"PAYMENT.CAPTURE.DENIED","resource_type":"capture","resource":{"id":"` + pay.CaptureID + `","status":"DECLINED","supplementary_data":{"related_ids":{"order_id":"` + orderID + `"}}}}`)
	pay, _ = e.st.GetPayment(orderID)
	pr, _ := e.st.GetProposal(pay.ProposalID)
	ms, _ := e.st.GetMission(msID)
	_, reserved := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentDenied || reserved != 0 || pr.Status != store.ProposalPending || ms.Status != store.MissionAwaitingApproval {
		t.Fatalf("bounced capture: pay=%s reserved=%v proposal=%s mission=%s", pay.Status, reserved, pr.Status, ms.Status)
	}
}

func TestReturnAfterTheWebhookCapturedFirst(t *testing.T) {
	// The CHECKOUT.ORDER.APPROVED webhook captured the order before the
	// buyer's browser came back; PayPal refuses the second capture and the
	// return handler must book what PayPal did instead of reporting failure.
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	_, _ = e.pp.Approve(orderID, true)
	e.webhook(`{"id":"WH-appr","event_type":"CHECKOUT.ORDER.APPROVED","resource_type":"checkout-order","resource":{"id":"` + orderID + `","status":"APPROVED"}}`)
	e.waitMission(msID, store.MissionPaid)
	spent, reserved := e.mandateNumbers(mdID)
	// the webhook is what booked it; now the buyer's browser arrives
	r, err := e.cli.Get(e.ts.URL + "/paypal/return?token=" + orderID)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if loc := r.Header.Get("Location"); !strings.HasSuffix(loc, "?payment=completed") {
		t.Fatalf("return after a webhook capture must report completed, got %s", loc)
	}
	pay, _ := e.st.GetPayment(orderID)
	spent2, reserved2 := e.mandateNumbers(mdID)
	if !pay.Accounted || pay.Status != store.PaymentCompleted || spent2 != spent || reserved2 != reserved || reserved != 0 {
		t.Fatalf("second arrival changed the books: %+v spent %v->%v reserved %v->%v", pay, spent, spent2, reserved, reserved2)
	}
	ev := e.missionEvents(msID)
	if strings.Count(ev, "payment captured") != 1 {
		t.Fatalf("the capture must be logged exactly once:\n%s", ev)
	}
}

// missionEvents returns the mission's activity log as text (tests).
func (e *env) missionEvents(msID string) string {
	e.t.Helper()
	var detail map[string]any
	e.get("/api/missions/"+msID, &detail)
	var b strings.Builder
	for _, ev := range detail["events"].([]any) {
		m := ev.(map[string]any)
		fmt.Fprintf(&b, "%s: %s\n", m["type"], m["message"])
	}
	return b.String()
}

func TestDoubleApprovalOnAOneClickMandateReturnsTheSameOrder(t *testing.T) {
	e := newEnv(t)
	e.pp.VaultCompletesOnCreate = true
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 400, "save_payment_method": true}, &md)
	mdID := md["id"].(string)
	mid, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(mid, store.MissionPaid)

	_, pid2 := e.pendingProposal(mdID)
	codes := make(chan int, 2)
	orders := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() {
			var out map[string]any
			codes <- e.post("/api/proposals/"+pid2+"/approve", nil, &out)
			if p, ok := out["payment"].(map[string]any); ok {
				orders <- p["id"].(string)
			} else {
				orders <- ""
			}
		}()
	}
	a, b := <-codes, <-codes
	oa, ob := <-orders, <-orders
	if a != 200 || b != 200 || oa == "" || oa != ob {
		t.Fatalf("double click on a one-click mandate: codes %d/%d orders %q/%q", a, b, oa, ob)
	}
	spent, _ := e.mandateNumbers(mdID)
	if spent != 79.9 {
		t.Fatalf("spent %v, want two carts", spent)
	}
}

func TestCancelLinkOnAPendingCaptureTellsTheTruth(t *testing.T) {
	e := newEnv(t)
	_, msID, orderID := e.startAutoApproved()
	var out map[string]any
	e.get("/api/missions/"+msID, &out)
	e.pp.NextCaptureStatus = "PENDING"
	e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string))
	r, err := e.cli.Get(e.ts.URL + "/paypal/cancel?token=" + orderID)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if loc := r.Header.Get("Location"); !strings.HasSuffix(loc, "?payment=pending") {
		t.Fatalf("a stale cancel link must not claim the order was cancelled, got %s", loc)
	}
}

func TestReturnAfterADeclineShowsTheDeniedBanner(t *testing.T) {
	e := newEnv(t)
	_, _, orderID := e.startAutoApproved()
	e.srv.markRefunded(orderID, store.PaymentDenied, 0, "") // the DENIED webhook came first
	r, err := e.cli.Get(e.ts.URL + "/paypal/return?token=" + orderID)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if loc := r.Header.Get("Location"); !strings.HasSuffix(loc, "?payment=denied") {
		t.Fatalf("got %s", loc)
	}
}

func TestRejectWithdrawsTheLiveOrderOfAPendingProposal(t *testing.T) {
	// Crash window: the order row was written, the proposal update was not.
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	mdID := md["id"].(string)
	msID, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	orderID := ap["payment"].(map[string]any)["id"].(string)
	_, _ = e.st.UpdateProposal(pid, func(p *store.Proposal) { p.Status = store.ProposalPending; p.OrderID = "" })
	_, _ = e.st.UpdateMission(msID, func(m *store.Mission) { m.Status = store.MissionAwaitingApproval; m.OrderID = "" })
	var rj map[string]any
	if code := e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": "no"}, &rj); code != 200 {
		t.Fatalf("reject: %d %v", code, rj)
	}
	pay, _ := e.st.GetPayment(orderID)
	_, reserved := e.mandateNumbers(mdID)
	if pay.Status != store.PaymentCancelled || pay.Reserved || reserved != 0 {
		t.Fatalf("orphaned order must be withdrawn: %+v reserved=%v", pay, reserved)
	}
	// and the buyer's late return never captures it
	r, err := e.cli.Get(e.ts.URL + "/paypal/return?token=" + orderID)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if pay, _ = e.st.GetPayment(orderID); pay.Status != store.PaymentCancelled {
		t.Fatalf("withdrawn order captured: %+v", pay)
	}
}

func TestDemoMandateIsCreatedOnce(t *testing.T) {
	e := newEnv(t)
	// five clicks at once: one creates, the rest get the same mandate back
	type res struct {
		code int
		id   string
		err  error
	}
	results := make(chan res, 5)
	for i := 0; i < 5; i++ {
		go func() {
			var out map[string]any
			code, err := e.tryPost("/api/mandates/demo", nil, &out)
			m, _ := out["mandate"].(map[string]any)
			id, _ := m["id"].(string)
			results <- res{code, id, err}
		}()
	}
	created, ids := 0, map[string]bool{}
	for i := 0; i < 5; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("demo: %v", r.err)
		}
		switch r.code {
		case 201:
			created++
		case 200:
		default:
			t.Fatalf("demo: %d", r.code)
		}
		ids[r.id] = true
	}
	if created != 1 || len(ids) != 1 {
		t.Fatalf("created %d demo mandates with ids %v", created, ids)
	}
	var md map[string]any
	if code := e.post("/api/mandates", map[string]any{"name": "Mine", "total_budget": 100, "demo": true}, &md); code != 201 || md["demo"] == true {
		t.Fatalf("the demo flag must not be settable through the API: %d %v", code, md)
	}
	var got map[string]any
	e.get("/api/mandates/"+md["id"].(string), &got)
	if got["mandate"].(map[string]any)["demo"] == true {
		t.Fatal("flag persisted")
	}
	var all []map[string]any
	e.get("/api/mandates", &all)
	if len(all) != 2 {
		t.Fatalf("mandates: %d", len(all))
	}
}

func TestConfiguredCurrencyIsNormalised(t *testing.T) {
	st, _ := store.Open("")
	t.Cleanup(st.Close)
	cat := catalog.NewMock()
	for in, want := range map[string]string{" usd ": "USD", "": "USD", "  ": "USD", "eur": "EUR"} {
		srv := New(Config{StaticDir: "/nonexistent", Currency: in}, st, paypal.NewMock("http://x"), agent.NewShopper(agent.Fake{}, cat, st), cat)
		srv.Close()
		if srv.cfg.Currency != want {
			t.Fatalf("CURRENCY=%q -> %q, want %q", in, srv.cfg.Currency, want)
		}
	}
}

func TestVaultTokenIsNeverExposed(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 400, "save_payment_method": true}, &md)
	mdID := md["id"].(string)
	mid, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(mid, store.MissionPaid)
	if e.mandateVault(mdID) == "" {
		t.Fatal("no token saved")
	}
	var one, detail map[string]any
	var all []map[string]any
	e.get("/api/mandates/"+mdID, &one)
	e.get("/api/mandates", &all)
	e.get("/api/missions/"+mid, &detail)
	for name, m := range map[string]map[string]any{"get": one["mandate"].(map[string]any), "list": all[0], "mission": detail["mandate"].(map[string]any)} {
		if v, ok := m["vault_id"]; ok && v != "" {
			t.Fatalf("%s exposes the vault token: %v", name, v)
		}
		if m["has_saved_payment"] != true {
			t.Fatalf("%s must say a saved account exists: %v", name, m)
		}
	}
	if strings.Contains(e.missionEvents(mid), e.mandateVault(mdID)) {
		t.Fatal("an event message carries the token")
	}
	for _, ev := range detail["events"].([]any) {
		if d, ok := ev.(map[string]any)["data"].(map[string]any); ok {
			if _, has := d["vault_id"]; has {
				t.Fatal("event data carries the token")
			}
		}
	}
}

func TestMandateInputIsBounded(t *testing.T) {
	e := newEnv(t)
	var out map[string]any
	for name, body := range map[string]map[string]any{
		"huge budget":    {"name": "x", "total_budget": 1e307},
		"huge cap":       {"name": "x", "total_budget": 100, "max_per_item": 1e300},
		"long name":      {"name": strings.Repeat("n", 101), "total_budget": 100},
		"many merchants": {"name": "x", "total_budget": 100, "blocked_merchants": strings.Split(strings.Repeat("a.com,", 21), ",")},
		"long category":  {"name": "x", "total_budget": 100, "allowed_categories": []string{strings.Repeat("c", 65)}},
	} {
		if code := e.post("/api/mandates", body, &out); code != 400 {
			t.Fatalf("%s: expected 400, got %d %v", name, code, out)
		}
	}
	if code := e.post("/api/mandates", map[string]any{"name": "ok", "total_budget": 1e9, "blocked_merchants": []string{"www.Temu.com/"}}, &out); code != 201 {
		t.Fatalf("largest allowed budget refused: %d %v", code, out)
	}
	// every page still renders JSON afterwards
	var ins map[string]any
	if code := e.get("/api/insights", &ins); code != 200 || ins["totals"] == nil {
		t.Fatalf("insights broken: %d %v", code, ins)
	}
}

func TestRejectionFeedbackIsTruncated(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	_, pid := e.pendingProposal(md["id"].(string))
	var rj map[string]any
	if code := e.post("/api/proposals/"+pid+"/reject", map[string]any{"reason": strings.Repeat("x", 5000)}, &rj); code != 200 {
		t.Fatalf("reject: %d %v", code, rj)
	}
	pr, _ := e.st.GetProposal(pid)
	if len(pr.Feedback) > MaxFeedbackLen+3 {
		t.Fatalf("feedback stored at %d chars", len(pr.Feedback))
	}
}

func TestPerClientLimitsOnThePublicDemo(t *testing.T) {
	st, _ := store.Open("")
	t.Cleanup(st.Close)
	cat := catalog.NewMock()
	ts := httptest.NewServer(nil)
	srv := New(Config{PublicURL: ts.URL, StaticDir: "/nonexistent", MaxMissionsPerHour: 40, PerClientLimits: true}, st, paypal.NewMock(ts.URL), agent.NewShopper(agent.Fake{}, cat, st), cat)
	ts.Config.Handler = srv.Handler()
	t.Cleanup(ts.Close)
	t.Cleanup(srv.Close)
	e := &env{t: t, ts: ts, st: st, cli: &http.Client{}}
	var out map[string]any
	for i := 0; i < 120; i++ {
		if code := e.get("/api/products/search?q=keyboard", &out); code != 200 {
			t.Fatalf("search %d: %d", i, code)
		}
	}
	if code := e.get("/api/products/search?q=keyboard", &out); code != 429 {
		t.Fatalf("121st search from one address must be refused, got %d", code)
	}
	for i := 0; i < 20; i++ {
		if code := e.post("/api/mandates", map[string]any{"name": "m", "total_budget": 100}, &out); code != 201 {
			t.Fatalf("mandate %d: %d %v", i, code, out)
		}
	}
	if code := e.post("/api/mandates", map[string]any{"name": "m", "total_budget": 100}, &out); code != 429 {
		t.Fatalf("21st mandate from one address must be refused, got %d", code)
	}
	// a forwarded address behind the compose proxy counts separately: the
	// proxy's own (last) entry is used, never what the client claimed
	req, _ := http.NewRequest("GET", ts.URL+"/api/products/search?q=keyboard", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, err := e.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("another client must not be limited by the first: %d", resp.StatusCode)
	}
	if got := clientIP(&http.Request{RemoteAddr: "10.0.0.2:4000", Header: http.Header{"X-Forwarded-For": {"1.2.3.4, 203.0.113.9"}}}); got != "203.0.113.9" {
		t.Fatalf("clientIP behind a proxy = %q", got)
	}
	if got := clientIP(&http.Request{RemoteAddr: "198.51.100.7:4000", Header: http.Header{"X-Forwarded-For": {"1.2.3.4"}}}); got != "198.51.100.7" {
		t.Fatalf("a public peer's header must be ignored: %q", got)
	}
	// one address gets three quarters of the cap (30 of 40); the global cap keeps the rest
	for i := 0; i < 30; i++ {
		if !srv.allowRunFrom(&http.Request{RemoteAddr: "203.0.113.50:1"}) {
			t.Fatalf("run %d from one address refused early", i)
		}
	}
	if srv.allowRunFrom(&http.Request{RemoteAddr: "203.0.113.50:1"}) {
		t.Fatal("the 31st run from one address must be refused")
	}
	if !srv.ips.room("run", "*", 40) || !srv.allowRunFrom(&http.Request{RemoteAddr: "203.0.113.51:1"}) {
		t.Fatal("another address must still be able to run")
	}
	// a refused run costs the client nothing: both slots move together
	srv2 := New(Config{StaticDir: "/nonexistent", MaxMissionsPerHour: 1, PerClientLimits: true}, st, paypal.NewMock(ts.URL), agent.NewShopper(agent.Fake{}, cat, st), cat)
	t.Cleanup(srv2.Close)
	_ = srv2.allowMissionStart() // the one global slot is gone
	for i := 0; i < 3; i++ {
		if msg := srv2.refuseRun(&http.Request{RemoteAddr: "203.0.113.60:1"}); !strings.Contains(msg, "allows 1 agent runs") {
			t.Fatalf("global cap must refuse with its own message, got %q", msg)
		}
	}
	if !srv2.ips.room("run", "203.0.113.60", 1) {
		t.Fatal("a globally refused run must not spend the client's own slot")
	}
	// with no global cap, runs are unlimited whatever the per-client switch says
	srv3 := New(Config{StaticDir: "/nonexistent", MaxMissionsPerHour: 0, PerClientLimits: true}, st, paypal.NewMock(ts.URL), agent.NewShopper(agent.Fake{}, cat, st), cat)
	t.Cleanup(srv3.Close)
	for i := 0; i < 15; i++ {
		if msg := srv3.refuseRun(&http.Request{RemoteAddr: "203.0.113.70:1"}); msg != "" {
			t.Fatalf("unlimited means unlimited, got %q", msg)
		}
	}
}

func TestRunNowRefusesForFree(t *testing.T) {
	st, _ := store.Open("")
	t.Cleanup(st.Close)
	cat := catalog.NewMock()
	ts := httptest.NewServer(nil)
	srv := New(Config{PublicURL: ts.URL, StaticDir: "/nonexistent", MaxMissionsPerHour: 40, PerClientLimits: true}, st, paypal.NewMock(ts.URL), agent.NewShopper(agent.Fake{}, cat, st), cat)
	ts.Config.Handler = srv.Handler()
	t.Cleanup(ts.Close)
	t.Cleanup(srv.Close)
	e := &env{t: t, ts: ts, st: st, cli: &http.Client{}}
	var md, ms map[string]any
	e.post("/api/mandates", map[string]any{"name": "Coffee", "total_budget": 100}, &md)
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress coffee maker"}, &ms)
	e.waitMission(ms["id"].(string), store.MissionAwaitingApproval)
	var out map[string]any
	for i := 0; i < 5; i++ {
		if code := e.post("/api/missions/"+ms["id"].(string)+"/run-now", nil, &out); code != 409 {
			t.Fatalf("run-now on a one-off mission: %d %v", code, out)
		}
	}
	if !srv.ips.room("run", "127.0.0.1", 2) { // one run so far: the mission itself
		t.Fatal("refused run-now attempts must not spend the client's slots")
	}
}

func TestSecurityHeadersAndCrossSitePosts(t *testing.T) {
	e := newEnv(t)
	resp, err := e.cli.Get(e.ts.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for h, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "strict-origin-when-cross-origin"} {
		if got := resp.Header.Get(h); got != want {
			t.Fatalf("%s = %q", h, got)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("csp = %q", csp)
	}
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/mandates/demo", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err = e.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("a cross-site POST must be refused, got %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", e.ts.URL+"/api/mandates/demo", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err = e.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("same-origin POST: %d", resp.StatusCode)
	}
}

func TestStandingOrderStopsWhenTheBudgetIsUsedUp(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Snacks", "total_budget": 39.95, "auto_approve_under": 50}, &md)
	mdID := md["id"].(string)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": mdID, "goal": "aeropress coffee maker", "repeat_days": 30}, &ms)
	msID := ms["id"].(string)
	out := e.waitMission(msID, store.MissionAwaitingPayment)
	e.payThroughMockCheckout(out["payments"].([]any)[0].(map[string]any)["approve_url"].(string))
	e.waitMission(msID, store.MissionPaid) // the whole budget is spent
	var run map[string]any
	if code := e.post("/api/missions/"+msID+"/run-now", nil, &run); code != 409 {
		t.Fatalf("run-now with no budget left: %d %v", code, run)
	}
	m, _ := e.st.GetMission(msID)
	if m.RepeatDays != 0 || m.NextRunAt != nil {
		t.Fatalf("standing order must stop: %+v", m)
	}
}

func TestOneClickPaymentSurvivesAClientDisconnect(t *testing.T) {
	// The browser goes away while the saved account is being charged: the
	// capture must still complete and be booked, never read as a decline.
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 400, "save_payment_method": true}, &md)
	mdID := md["id"].(string)
	mid, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	e.payThroughMockCheckout(ap["approve_url"].(string))
	e.waitMission(mid, store.MissionPaid)
	vault := e.mandateVault(mdID)

	mid2, pid2 := e.pendingProposal(mdID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the request is already gone
	pay, err := e.srv.createPayment(ctx, pid2)
	if err != nil {
		t.Fatalf("a cancelled request must not abandon the charge: %v", err)
	}
	if !pay.Accounted || pay.Status != store.PaymentCompleted {
		t.Fatalf("not booked: %+v", pay)
	}
	if e.mandateVault(mdID) != vault {
		t.Fatal("the saved account must survive")
	}
	e.waitMission(mid2, store.MissionPaid)
}

func TestNegotiationUnderACategoryRuleFailsFastWithASuggestion(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Desk refresh", "total_budget": 300, "allowed_categories": []string{"computers", "office"}}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "A two-hour product photo shoot", "kind": "negotiate"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionNoProposal)
	m := out["mission"].(map[string]any)
	sug, _ := m["suggestion"].(map[string]any)
	if sug == nil || sug["field"] != "allowed_categories" {
		t.Fatalf("expected the services suggestion, got %v", m["suggestion"])
	}
	if n, _ := m["negotiation"].(map[string]any); n != nil && len(n["rounds"].([]any)) > 0 {
		t.Fatalf("no negotiation round should have been spent: %v", n)
	}
	// applying the suggestion retries and this time the deal goes through
	var retry map[string]any
	if code := e.post("/api/missions/"+ms["id"].(string)+"/apply-suggestion", nil, &retry); code != 201 {
		t.Fatalf("apply: %d %v", code, retry)
	}
	e.waitMission(retry["mission"].(map[string]any)["id"].(string), store.MissionAwaitingApproval)
}

func TestStartupBooksACaptureTheLedgerMissed(t *testing.T) {
	// A row from an older build: PayPal captured the vault order at creation,
	// the row says COMPLETED, but nothing was booked and the mission still
	// waits for payment. A restart must repair it without anyone clicking.
	e := newEnv(t)
	mdID, msID, orderID := e.startAutoApproved()
	_, _ = e.pp.Approve(orderID, true)
	if _, err := e.pp.CaptureOrder(context.Background(), orderID); err != nil {
		t.Fatal(err)
	}
	_, _ = e.st.UpdatePayment(orderID, func(x *store.Payment) { x.Status = store.PaymentCompleted; x.Accounted = false; x.Reserved = true })
	e.srv.recomputeReserved(mdID)
	if _, reserved := e.mandateNumbers(mdID); reserved == 0 {
		t.Fatal("setup: the stale row must still hold its reservation")
	}
	cat := catalog.NewMock()
	srv2 := New(Config{PublicURL: e.ts.URL, StaticDir: "/nonexistent", Currency: "USD"}, e.st, e.pp, agent.NewShopper(agent.Fake{}, cat, e.st), cat)
	t.Cleanup(srv2.Close)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pay, _ := e.st.GetPayment(orderID); pay.Accounted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	ms, _ := e.st.GetMission(msID)
	if !pay.Accounted || spent != pay.Amount || reserved != 0 || ms.Status != store.MissionPaid {
		t.Fatalf("startup reconcile: %+v spent=%v reserved=%v mission=%s", pay, spent, reserved, ms.Status)
	}
}
