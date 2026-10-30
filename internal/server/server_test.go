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
	return &env{t: t, ts: ts, st: st, cli: cli}
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
