package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"mandate/internal/agent"
	"mandate/internal/paypal"
)

// The order assistant answers questions about purchases made under the
// user's mandates. It reads Mandate's own ledger and, through the Anthropic
// MCP connector, PayPal's hosted MCP server (the PayPal Agent Toolkit). Only
// read-only PayPal tools are enabled: the assistant can look at orders,
// transactions, tracking, refunds, disputes and invoices, never create or
// pay anything. Payments still happen only through human approval.

// tokenSource is implemented by the REST PayPal client (not the mock): it
// provides the OAuth2 token PayPal's MCP server accepts and the MCP endpoint.
type tokenSource interface {
	AccessToken(ctx context.Context) (string, error)
	MCPURL() string
}

// probeMCP is swapped in tests.
var probeMCP = paypal.ProbeMCP

type mcpState struct {
	mu        sync.Mutex
	enabled   bool
	url       string   // endpoint handed to the Anthropic MCP connector
	tools     []string // read-only tools enabled for the model
	available int      // tools the server offers in total
	err       string
	checkedAt time.Time
	done      chan struct{}
}

// initMCP decides whether the order assistant can run and, if so, checks
// PayPal's MCP server in the background so a slow or refused handshake never
// delays startup or breaks shopping missions.
func (s *Server) initMCP() {
	s.mcp.done = make(chan struct{})
	mode := strings.ToLower(strings.TrimSpace(s.cfg.PayPalMCP))
	ts, hasToken := s.pp.(tokenSource)
	_, capable := s.shopper.Provider.(agent.MCPCapable)
	switch {
	case mode == "off":
		s.setMCP(false, "", nil, 0, "disabled with PAYPAL_MCP=off")
	case !hasToken:
		s.setMCP(false, "", nil, 0, "PayPal runs in mock mode; the assistant needs sandbox or live credentials")
	case !capable:
		s.setMCP(false, "", nil, 0, "the agent provider cannot use MCP (offline agent or a compatibility gateway)")
	default:
		url := s.cfg.PayPalMCPURL
		if url == "" {
			// PayPal documents the SSE endpoint for the Anthropic connector.
			url = strings.TrimSuffix(ts.MCPURL(), "/http") + "/sse"
		}
		go s.probeMCPServer(ts, url)
		return
	}
	close(s.mcp.done)
}

func (s *Server) probeMCPServer(ts tokenSource, connectorURL string) {
	defer close(s.mcp.done)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := ts.AccessToken(ctx)
	if err != nil {
		s.setMCP(false, connectorURL, nil, 0, "PayPal token: "+err.Error())
		log.Printf("PayPal MCP: disabled: %v", err)
		return
	}
	tools, err := probeMCP(ctx, nil, ts.MCPURL(), tok)
	if err != nil {
		s.setMCP(false, connectorURL, nil, 0, "PayPal MCP server handshake failed: "+err.Error())
		log.Printf("PayPal MCP: disabled: %v", err)
		return
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	allowed := agent.ReadOnlyMCPTools(names)
	if len(allowed) == 0 {
		s.setMCP(false, connectorURL, nil, len(tools), "PayPal MCP server offered no read-only tools")
		log.Printf("PayPal MCP: disabled: no read-only tools among %d", len(tools))
		return
	}
	s.setMCP(true, connectorURL, allowed, len(tools), "")
	log.Printf("PayPal MCP: order assistant ready: %d read-only tools enabled of %d offered (%s)", len(allowed), len(tools), strings.Join(allowed, ", "))
}

func (s *Server) setMCP(enabled bool, url string, tools []string, available int, errMsg string) {
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	s.mcp.enabled, s.mcp.url, s.mcp.tools, s.mcp.available, s.mcp.err, s.mcp.checkedAt = enabled, url, tools, available, errMsg, time.Now()
}

func (s *Server) mcpStatus() map[string]any {
	s.mcp.mu.Lock()
	defer s.mcp.mu.Unlock()
	out := map[string]any{"enabled": s.mcp.enabled, "tools": append([]string{}, s.mcp.tools...), "available": s.mcp.available}
	if s.mcp.err != "" {
		out["error"] = s.mcp.err
	}
	if s.mcp.enabled {
		out["url"] = s.mcp.url
	}
	if !s.mcp.checkedAt.IsZero() {
		out["checked_at"] = s.mcp.checkedAt
	} else {
		out["checking"] = true
	}
	return out
}

// AskSystemPrompt is the order assistant's instructions.
const AskSystemPrompt = `You are Mandate's order assistant. Mandate is an AI shopping agent that can only spend inside a human-defined mandate (budget, caps, rules) and pays through PayPal after human approval.

You answer questions about the user's mandates and the purchases made under them. You have two sources:
1. LEDGER (below): Mandate's own records of mandates, missions and PayPal orders, with PayPal order IDs and capture IDs.
2. PayPal MCP tools: read-only access to PayPal itself (orders, transactions, shipment tracking, refunds, disputes, invoices). Use them to confirm or enrich what the ledger says, for example get_order with an order ID from the ledger, or list_transactions for a date range. Transaction reporting can lag by a few hours; say so if a recent payment is missing there.

Rules:
- You cannot and must not create, pay, capture, refund, cancel or modify anything. If asked to, explain that money only moves through human approval in Mandate.
- Answer in plain text with short bullet lists. Quote IDs, amounts and dates exactly. Never invent data: if neither the ledger nor PayPal has it, say so.
- Keep answers short: lead with the answer, then the evidence (which ledger rows or PayPal results you used).
- If a PayPal tool returns an error or nothing useful, say so in one line and answer from the ledger; never stall on a failing tool.
- Amounts in the ledger are in the mandate's currency; PayPal's sandbox environment is a test environment, so payer accounts look like sb-xxxx@personal.example.com.`

type askRequest struct {
	Question  string `json:"question"`
	MandateID string `json:"mandate_id"`
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var in askRequest
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	in.Question = strings.TrimSpace(in.Question)
	if in.Question == "" {
		writeErr(w, 400, "question is required")
		return
	}
	if utf8Len(in.Question) > 600 {
		writeErr(w, 400, "question is too long (600 characters max)")
		return
	}
	s.mcp.mu.Lock()
	enabled, url, tools, errMsg := s.mcp.enabled, s.mcp.url, append([]string{}, s.mcp.tools...), s.mcp.err
	s.mcp.mu.Unlock()
	if !enabled {
		if errMsg == "" {
			errMsg = "still checking PayPal's MCP server, try again in a moment"
		}
		writeErr(w, 503, "the order assistant is not available: "+errMsg)
		return
	}
	capable, _ := s.shopper.Provider.(agent.MCPCapable)
	ts, _ := s.pp.(tokenSource)
	if capable == nil || ts == nil {
		writeErr(w, 503, "the order assistant is not available")
		return
	}
	if !s.allowMissionStart() {
		writeErr(w, 429, "too many agent runs this hour; please try again later")
		return
	}

	system := AskSystemPrompt + "\n\nToday is " + time.Now().UTC().Format("2006-01-02") + " (UTC).\n\nLEDGER:\n" + s.ledgerContext(in.MandateID)
	sess := capable.NewMCPSession(system, agent.MCPServer{Name: "paypal", URL: url, Token: ts.AccessToken, AllowedTools: tools})

	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	var events []map[string]any
	var used []string
	started := time.Now()
	answer, err := sess.Send(ctx, in.Question, nil, func(e agent.Event) {
		ev := map[string]any{"type": e.Type, "message": e.Message}
		if e.Data != nil {
			ev["data"] = e.Data
		}
		events = append(events, ev)
		if e.Type == "mcp_tool_call" {
			if d, ok := e.Data.(map[string]any); ok {
				if name, _ := d["tool"].(string); name != "" {
					used = append(used, name)
				}
			}
		}
	})
	log.Printf("order assistant: %d chars in %s, tools=%v err=%v", len(answer), time.Since(started).Round(time.Millisecond), used, err)
	if err != nil && strings.TrimSpace(answer) == "" {
		writeErr(w, 502, "the assistant failed: "+err.Error())
		return
	}
	resp := map[string]any{"answer": answer, "tools_used": uniq(used), "events": events}
	if err != nil {
		resp["warning"] = err.Error()
	}
	writeJSON(w, 200, resp)
}

// ledgerContext renders the mandates and payments the assistant may talk
// about as compact JSON (most recent 40 payments).
func (s *Server) ledgerContext(mandateID string) string {
	type row struct {
		OrderID     string  `json:"paypal_order_id"`
		CaptureID   string  `json:"capture_id,omitempty"`
		Status      string  `json:"status"`
		Amount      float64 `json:"amount"`
		Currency    string  `json:"currency"`
		Mandate     string  `json:"mandate"`
		Goal        string  `json:"mission_goal"`
		Items       int     `json:"items"`
		Payer       string  `json:"payer,omitempty"`
		PaidVia     string  `json:"paid_via"`
		CreatedAt   string  `json:"created_at"`
		Environment string  `json:"environment"`
	}
	mandates := s.st.ListMandates()
	mdByID := map[string]string{}
	var mds []map[string]any
	for _, m := range mandates {
		if mandateID != "" && m.ID != mandateID {
			continue
		}
		mdByID[m.ID] = m.Name
		mds = append(mds, map[string]any{"id": m.ID, "name": m.Name, "currency": m.Currency, "budget": m.TotalBudget, "spent": m.Spent, "reserved": m.Reserved,
			"remaining": m.Remaining(), "max_per_item": m.MaxPerItem, "auto_approve_under": m.AutoApproveUnder, "allowed_categories": m.AllowedCategories,
			"blocked_merchants": m.BlockedMerchants, "min_rating": m.MinRating, "saved_paypal_account": m.HasSavedPayment()})
	}
	pays := s.st.ListPayments()
	sort.Slice(pays, func(i, j int) bool { return pays[i].CreatedAt.After(pays[j].CreatedAt) })
	var rows []row
	for _, p := range pays {
		if _, ok := mdByID[p.MandateID]; !ok {
			continue
		}
		goal, items := "", 0
		if ms, err := s.st.GetMission(p.MissionID); err == nil {
			goal = ms.Goal
		}
		if pr, err := s.st.GetProposal(p.ProposalID); err == nil {
			items = len(pr.Cart.Items)
		}
		via := "PayPal checkout"
		if p.Vaulted {
			via = "saved PayPal account (no checkout)"
		}
		rows = append(rows, row{OrderID: p.ID, CaptureID: p.CaptureID, Status: p.Status, Amount: p.Amount, Currency: p.Currency, Mandate: mdByID[p.MandateID],
			Goal: goal, Items: items, Payer: p.PayerEmail, PaidVia: via, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339), Environment: p.Mode})
		if len(rows) >= 40 {
			break
		}
	}
	b, _ := json.MarshalIndent(map[string]any{"mandates": mds, "payments": rows}, "", " ")
	return string(b)
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func utf8Len(s string) int { return len([]rune(s)) }
