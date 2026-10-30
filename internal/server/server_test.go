package server

import (
	"bytes"
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
	"mandate/internal/paypal"
	"mandate/internal/store"
)

type env struct {
	t   *testing.T
	ts  *httptest.Server
	st  *store.Store
	cli *http.Client
	pp  *paypal.Mock
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
	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &env{t: t, ts: ts, st: st, cli: cli, pp: pp}
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

func (e *env) mandateNumbers(id string) (spent, reserved float64) {
	var mdv map[string]any
	e.get("/api/mandates/"+id, &mdv)
	m := mdv["mandate"].(map[string]any)
	return m["spent"].(float64), m["reserved"].(float64)
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
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var out map[string]any
		e.get("/api/missions/"+id, &out)
		m := out["mission"].(map[string]any)
		if m["status"] == status {
			return out
		}
		if m["status"] == store.MissionFailed {
			e.t.Fatalf("mission failed: %v", m["error"])
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("mission %s never reached %s", id, status)
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
	if len(pays) != 1 || pays[0].(map[string]any)["status"] != "COMPLETED" || pays[0].(map[string]any)["capture_id"] == "" {
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
	// a paid proposal cannot be approved again
	if code := e.post("/api/proposals/"+pid+"/approve", nil, &ap); code != 409 {
		t.Fatalf("re-approve paid: %d", code)
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
	if pay["status"] != "COMPLETED" || pay["capture_id"] == "" {
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
	if pay["status"] != "COMPLETED" || pay["capture_id"] == "" {
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
	pay, _ := e.st.GetPayment(orderID)
	spent, reserved := e.mandateNumbers(mdID)
	if pay.Status != "COMPLETED" || spent != pay.Amount || reserved != 0 {
		t.Fatalf("late capture: status=%s spent=%v reserved=%v amount=%v", pay.Status, spent, reserved, pay.Amount)
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
// follow the redirect back to /paypal/return.
func (e *env) payThroughMockCheckout(approveURL string) {
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
}

func (e *env) mandateVault(id string) string {
	var mdv map[string]any
	e.get("/api/mandates/"+id, &mdv)
	v, _ := mdv["mandate"].(map[string]any)["vault_id"].(string)
	return v
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
	if pay["status"] != "COMPLETED" || pay["vaulted"] != true || pay["capture_id"] == "" || pay["payer_email"] == "" {
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
	// approving the paid proposal again is refused
	if code := e.post("/api/proposals/"+pid2+"/approve", nil, &ap); code != 409 {
		t.Fatalf("re-approve: %d", code)
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
