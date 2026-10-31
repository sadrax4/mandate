package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mandate/internal/agent"
	"mandate/internal/catalog"
	"mandate/internal/paypal"
	"mandate/internal/store"
)

// mcpPayPal is the mock PayPal client plus the token/MCP surface of the REST client.
type mcpPayPal struct{ *paypal.Mock }

func (mcpPayPal) AccessToken(context.Context) (string, error) { return "mock-token", nil }
func (mcpPayPal) MCPURL() string                              { return "https://mcp.sandbox.paypal.com/http" }

// mcpProvider is a scripted MCP-capable provider that records what it was given.
type mcpProvider struct {
	agent.Fake
	got  agent.MCPServer
	seen string
}

func (p *mcpProvider) SupportsMCP() bool { return true }

func (p *mcpProvider) NewMCPSession(system string, m agent.MCPServer) agent.Session {
	p.got = m
	p.seen = system
	return &scriptedAsk{p: p}
}

type scriptedAsk struct{ p *mcpProvider }

func (s *scriptedAsk) Send(_ context.Context, q string, _ []agent.Tool, emit func(agent.Event)) (string, error) {
	emit(agent.Event{Type: "mcp_tool_call", Message: "paypal: get_order", Data: map[string]any{"server": "paypal", "tool": "get_order", "input": json.RawMessage(`{"id":"X"}`)}})
	emit(agent.Event{Type: "mcp_tool_result", Message: "result ok", Data: map[string]any{"preview": `{"status":"COMPLETED"}`}})
	return "Order X is COMPLETED (asked: " + q + ")", nil
}

func TestAskIsUnavailableInMockMode(t *testing.T) {
	e := newEnv(t)
	var out map[string]any
	if code := e.post("/api/ask", map[string]any{"question": "what did I buy?"}, &out); code != 503 {
		t.Fatalf("expected 503 in mock mode, got %d %v", code, out)
	}
	var cfg map[string]any
	e.get("/api/config", &cfg)
	if cfg["paypal_mcp"].(map[string]any)["enabled"] != false {
		t.Fatalf("config = %v", cfg["paypal_mcp"])
	}
}

func TestAskUsesReadOnlyPayPalMCPTools(t *testing.T) {
	// swap the probe for a scripted PayPal MCP server listing
	probeMCPSaved := probeMCP
	probeMCP = func(_ context.Context, _ *http.Client, url, token string) ([]paypal.MCPTool, string, error) {
		if token != "mock-token" || !strings.HasSuffix(url, "/http") {
			t.Errorf("probe called with url=%s token=%s", url, token)
		}
		return []paypal.MCPTool{{Name: "get_order"}, {Name: "create_order"}, {Name: "pay_order"}, {Name: "list_transactions"}, {Name: "create_refund"}, {Name: "get_refund"}}, "sse", nil
	}
	t.Cleanup(func() { probeMCP = probeMCPSaved })

	st, _ := store.Open("")
	cat := catalog.NewMock()
	prov := &mcpProvider{}
	shopper := agent.NewShopper(prov, cat, st)
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	t.Cleanup(ts.Close)
	pp := mcpPayPal{paypal.NewMock(ts.URL)}
	srv := New(Config{PublicURL: ts.URL, StaticDir: "/nonexistent", Currency: "USD"}, st, pp, shopper, cat)
	ts.Config.Handler = srv.Handler()
	select {
	case <-srv.mcp.done:
	case <-time.After(5 * time.Second):
		t.Fatal("probe never finished")
	}
	e := &env{t: t, ts: ts, st: st, cli: ts.Client()}

	var cfg map[string]any
	e.get("/api/config", &cfg)
	mcp := cfg["paypal_mcp"].(map[string]any)
	if mcp["enabled"] != true || mcp["url"] != "https://mcp.sandbox.paypal.com/sse" || mcp["transport"] != "sse" {
		t.Fatalf("mcp config = %v", mcp)
	}
	tools := mcp["tools"].([]any)
	if len(tools) != 3 || tools[0] != "get_order" || tools[1] != "get_refund" || tools[2] != "list_transactions" {
		t.Fatalf("enabled tools = %v", tools)
	}

	// a mandate and a paid order give the assistant something to talk about
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 200, "auto_approve_under": 60}, &md)
	var ms map[string]any
	e.post("/api/missions", map[string]any{"mandate_id": md["id"], "goal": "aeropress coffee maker"}, &ms)
	out := e.waitMission(ms["id"].(string), store.MissionAwaitingPayment)
	orderID := out["payments"].([]any)[0].(map[string]any)["id"].(string)

	var ans map[string]any
	if code := e.post("/api/ask", map[string]any{"question": "did my coffee order go through?"}, &ans); code != 200 {
		t.Fatalf("ask: %d %v", code, ans)
	}
	if !strings.Contains(ans["answer"].(string), "COMPLETED") || ans["tools_used"].([]any)[0] != "get_order" {
		t.Fatalf("answer = %v", ans)
	}
	if len(ans["events"].([]any)) != 2 {
		t.Fatalf("events = %v", ans["events"])
	}
	// the session got the read-only allowlist, the connector URL and a working token
	if got := prov.got; got.Name != "paypal" || got.URL != "https://mcp.sandbox.paypal.com/sse" || len(got.AllowedTools) != 3 {
		t.Fatalf("mcp server handed to the session = %+v", got)
	}
	if tok, err := prov.got.Token(context.Background()); err != nil || tok != "mock-token" {
		t.Fatalf("token = %q %v", tok, err)
	}
	if !strings.Contains(prov.seen, orderID) || !strings.Contains(prov.seen, "Household") {
		t.Fatalf("system prompt lacks the ledger: %s", prov.seen)
	}
	if code := e.post("/api/ask", map[string]any{"question": ""}, &ans); code != 400 {
		t.Fatalf("empty question: %d", code)
	}
}

func TestWebhookVaultTokenCreatedSavesAccount(t *testing.T) {
	e := newEnv(t)
	var md map[string]any
	e.post("/api/mandates", map[string]any{"name": "Household", "total_budget": 300, "save_payment_method": true}, &md)
	mdID := md["id"].(string)
	mid, pid := e.pendingProposal(mdID)
	var ap map[string]any
	e.post("/api/proposals/"+pid+"/approve", nil, &ap)
	orderID := ap["payment"].(map[string]any)["id"].(string)

	body := `{"id":"WH-1","event_type":"VAULT.PAYMENT-TOKEN.CREATED","resource_type":"payment_token","resource":{"id":"tok_abc","customer":{"id":"c1"},"payment_source":{"paypal":{"email_address":"buyer@example.com"}},"metadata":{"order_id":"` + orderID + `"}}}`
	req, _ := http.NewRequest("POST", e.ts.URL+"/paypal/webhook", strings.NewReader(body))
	req.Header.Set("Paypal-Transmission-Sig", "mock")
	resp, err := e.cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("webhook: %d", resp.StatusCode)
	}
	if v := e.mandateVault(mdID); v != "tok_abc" {
		t.Fatalf("vault = %q", v)
	}
	var detail map[string]any
	e.get("/api/missions/"+mid, &detail)
	var sawVault bool
	for _, ev := range detail["events"].([]any) {
		if ev.(map[string]any)["type"] == "vault" {
			sawVault = true
		}
	}
	if !sawVault {
		t.Fatal("vault event missing")
	}
}
