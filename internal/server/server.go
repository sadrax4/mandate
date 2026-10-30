// Package server exposes the HTTP API, the PayPal return/cancel endpoints, the
// server-sent event stream for live agent activity, the mock PayPal approval
// page used in offline demos, and the static web UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mandate/internal/agent"
	"mandate/internal/catalog"
	"mandate/internal/mandate"
	"mandate/internal/paypal"
	"mandate/internal/store"
)

// Config holds runtime settings.
type Config struct {
	PublicURL string
	StaticDir string
	Currency  string
	Version   string
	// WebhookID is the PayPal webhook ID used to verify deliveries to /paypal/webhook.
	WebhookID string
	// MaxMissionsPerHour caps how many agent runs may start per hour across
	// the whole server, so a public demo cannot drain the model budget.
	// 0 disables the cap.
	MaxMissionsPerHour int
	// DemoBuyerHint is shown next to the PayPal button so testers know which
	// sandbox buyer account to log in with (never used for anything else).
	DemoBuyerHint string
	// PayPalMCP controls the order assistant backed by PayPal's MCP server:
	// "auto" (default: on when PayPal credentials and an MCP-capable agent
	// provider exist), "on" or "off".
	PayPalMCP string
	// PayPalMCPURL overrides the MCP endpoint handed to the Anthropic MCP
	// connector (default: PayPal's SSE endpoint for the current environment).
	PayPalMCPURL string
}

// Server is the HTTP layer.
type Server struct {
	cfg     Config
	st      *store.Store
	pp      paypal.Client
	ppMock  *paypal.Mock
	shopper *agent.Shopper
	catalog catalog.Searcher
	mux     *http.ServeMux

	rateMu     sync.Mutex
	rateStarts []time.Time

	// payMu serialises every money-moving transition (approve, capture,
	// cancel, void, refund) so a double click or a webhook racing a redirect
	// can never reserve or spend the same amount twice.
	payMu sync.Mutex

	mcp mcpState
}

// New wires the handlers.
func New(cfg Config, st *store.Store, pp paypal.Client, shopper *agent.Shopper, cat catalog.Searcher) *Server {
	if cfg.Currency == "" {
		cfg.Currency = "USD"
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	s := &Server{cfg: cfg, st: st, pp: pp, shopper: shopper, catalog: cat, mux: http.NewServeMux()}
	if m, ok := pp.(*paypal.Mock); ok {
		s.ppMock = m
	}
	shopper.OnProposal = func(ctx context.Context, p *store.Proposal) error {
		if p.Status != store.ProposalAutoApproved {
			return nil
		}
		_, err := s.createPayment(ctx, p.ID)
		return err
	}
	s.initMCP()
	s.routes()
	go s.scheduler()
	return s
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler { return logRequests(s.mux) }

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	m.HandleFunc("GET /api/config", s.handleConfig)
	m.HandleFunc("GET /api/categories", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, catalog.Categories) })
	m.HandleFunc("GET /api/products/search", s.handleSearch)

	m.HandleFunc("GET /api/mandates", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.st.ListMandates()) })
	m.HandleFunc("POST /api/mandates", s.handleCreateMandate)
	m.HandleFunc("GET /api/mandates/{id}", s.handleGetMandate)
	m.HandleFunc("POST /api/mandates/{id}/forget-payment", s.handleForgetPayment)

	m.HandleFunc("GET /api/missions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.st.ListMissions(r.URL.Query().Get("mandate_id")))
	})
	m.HandleFunc("POST /api/missions", s.handleCreateMission)
	m.HandleFunc("GET /api/missions/{id}", s.handleGetMission)
	m.HandleFunc("GET /api/missions/{id}/events", s.handleEvents)
	m.HandleFunc("POST /api/missions/{id}/apply-suggestion", s.handleApplySuggestion)
	m.HandleFunc("POST /api/missions/{id}/run-now", s.handleRunNow)
	m.HandleFunc("POST /api/missions/{id}/stop-repeat", s.handleStopRepeat)

	m.HandleFunc("POST /api/proposals/{id}/approve", s.handleApprove)
	m.HandleFunc("POST /api/proposals/{id}/reject", s.handleReject)
	m.HandleFunc("GET /api/ledger", s.handleLedger)
	m.HandleFunc("GET /api/insights", s.handleInsights)
	m.HandleFunc("POST /api/payments/{id}/sync", s.handleSyncPayment)
	m.HandleFunc("POST /api/ask", s.handleAsk)

	m.HandleFunc("GET /paypal/return", s.handlePayPalReturn)
	m.HandleFunc("GET /paypal/cancel", s.handlePayPalCancel)
	m.HandleFunc("POST /paypal/webhook", s.handlePayPalWebhook)

	if s.ppMock != nil {
		m.HandleFunc("GET /mock/paypal/approve", s.handleMockApprovePage)
		m.HandleFunc("POST /mock/paypal/approve", s.handleMockApprove)
	}
	m.Handle("/", s.staticHandler())
}

// --- config & catalog ---

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"paypal_mode":     s.pp.Mode(),
		"llm_provider":    s.shopper.Provider.Name(),
		"catalog":         s.catalog.Name(),
		"currency":        s.cfg.Currency,
		"public_url":      s.cfg.PublicURL,
		"version":         s.cfg.Version,
		"demo_buyer_hint": s.cfg.DemoBuyerHint,
		"paypal_mcp":      s.mcpStatus(),
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	maxPrice, _ := strconv.ParseFloat(q.Get("max_price"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	res, err := s.catalog.Search(r.Context(), catalog.Query{Text: q.Get("q"), MaxPrice: maxPrice, Category: q.Get("category"), Limit: limit, Currency: q.Get("currency")})
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

// --- mandates ---

func (s *Server) handleCreateMandate(w http.ResponseWriter, r *http.Request) {
	var in mandate.Mandate
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.Currency == "" {
		in.Currency = s.cfg.Currency
	}
	in.Currency = strings.ToUpper(in.Currency)
	in.ID = store.NewID("mnd")
	in.Spent, in.Reserved = 0, 0
	in.VaultID, in.VaultPayer, in.VaultedAt = "", "", nil // only a real checkout can save an account
	in.CreatedAt = time.Now()
	in.AllowedCategories = cleanList(in.AllowedCategories)
	in.BlockedMerchants = cleanList(in.BlockedMerchants)
	if err := in.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.st.PutMandate(&in)
	writeJSON(w, 201, in)
}

// handleForgetPayment revokes the saved PayPal account on a mandate: the
// agent loses the ability to pay without a human until a future checkout
// saves a new one.
func (s *Server) handleForgetPayment(w http.ResponseWriter, r *http.Request) {
	s.payMu.Lock()
	defer s.payMu.Unlock()
	md, err := s.st.GetMandate(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "mandate not found")
		return
	}
	if !md.HasSavedPayment() {
		writeErr(w, 409, "no saved PayPal account on this mandate")
		return
	}
	_ = s.st.UpdateMandate(md.ID, func(m *mandate.Mandate) error { m.VaultID, m.VaultPayer, m.VaultedAt = "", "", nil; return nil })
	for _, ms := range s.st.ListMissions(md.ID) {
		s.st.AddEvent(ms.ID, "vault", "the human removed the saved PayPal account from this mandate; future carts go through PayPal checkout", nil)
		break
	}
	md, _ = s.st.GetMandate(md.ID)
	writeJSON(w, 200, md)
}

func (s *Server) handleGetMandate(w http.ResponseWriter, r *http.Request) {
	md, err := s.st.GetMandate(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "mandate not found")
		return
	}
	writeJSON(w, 200, map[string]any{"mandate": md, "missions": s.st.ListMissions(md.ID)})
}

// --- missions ---

func (s *Server) handleCreateMission(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MandateID  string `json:"mandate_id"`
		Goal       string `json:"goal"`
		RepeatDays int    `json:"repeat_days"`
		Kind       string `json:"kind"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	in.Goal = strings.TrimSpace(in.Goal)
	if in.Goal == "" {
		writeErr(w, 400, "goal is required")
		return
	}
	if in.RepeatDays < 0 || in.RepeatDays > 365 {
		writeErr(w, 400, "repeat_days must be between 1 and 365 (0 = run once)")
		return
	}
	if in.Kind == "" {
		in.Kind = "buy"
	}
	if in.Kind != "buy" && in.Kind != "negotiate" {
		writeErr(w, 400, "kind must be buy or negotiate")
		return
	}
	if len(in.Goal) > 2000 {
		writeErr(w, 400, "goal is too long")
		return
	}
	if _, err := s.st.GetMandate(in.MandateID); err != nil {
		writeErr(w, 404, "mandate not found")
		return
	}
	if !s.allowMissionStart() {
		writeErr(w, 429, fmt.Sprintf("this demo allows %d agent runs per hour; please try again later", s.cfg.MaxMissionsPerHour))
		return
	}
	m := &store.Mission{ID: store.NewID("msn"), MandateID: in.MandateID, Goal: in.Goal, Status: store.MissionRunning, Kind: in.Kind, CreatedAt: time.Now()}
	if in.RepeatDays > 0 {
		next := time.Now().Add(time.Duration(in.RepeatDays) * 24 * time.Hour)
		m.RepeatDays, m.NextRunAt = in.RepeatDays, &next
	}
	s.st.PutMission(m)
	s.st.AddEvent(m.ID, "status", "mission created: "+in.Goal, nil)
	if m.RepeatDays > 0 {
		s.st.AddEvent(m.ID, "status", fmt.Sprintf("standing order: this goal runs again every %d day(s), next on %s; each run is checked against the mandate like any other", m.RepeatDays, m.NextRunAt.UTC().Format("2006-01-02")), map[string]any{"repeat_days": m.RepeatDays, "next_run_at": m.NextRunAt})
	}
	s.startAgent(m, "")
	writeJSON(w, 201, m)
}

// startAgent runs a mission in the background according to its kind.
func (s *Server) startAgent(m *store.Mission, feedback string) {
	if m.Kind == "negotiate" {
		go s.shopper.Negotiate(context.Background(), m.ID, feedback)
		return
	}
	go s.shopper.Run(context.Background(), m.ID)
}

// --- standing orders ---

// startRun runs a standing order's goal once more as a new mission and
// schedules the next run. It is used by the scheduler and by "Run now".
func (s *Server) startRun(parent *store.Mission, why string) (*store.Mission, error) {
	if parent.RepeatDays <= 0 {
		return nil, &httpError{409, "this mission is not a standing order"}
	}
	if !s.allowMissionStart() {
		return nil, &httpError{429, fmt.Sprintf("this demo allows %d agent runs per hour; please try again later", s.cfg.MaxMissionsPerHour)}
	}
	run := &store.Mission{ID: store.NewID("msn"), MandateID: parent.MandateID, Goal: parent.Goal, Status: store.MissionRunning, Kind: parent.Kind, RepeatOf: parent.ID, CreatedAt: time.Now()}
	s.st.PutMission(run)
	next := time.Now().Add(time.Duration(parent.RepeatDays) * 24 * time.Hour)
	_, _ = s.st.UpdateMission(parent.ID, func(m *store.Mission) {
		m.LastRunID = run.ID
		if m.RepeatDays > 0 {
			m.NextRunAt = &next
		}
	})
	s.st.AddEvent(parent.ID, "status", fmt.Sprintf("standing order run started (%s): mission %s; next run on %s", why, run.ID, next.UTC().Format("2006-01-02")), map[string]any{"run_mission_id": run.ID, "next_run_at": next})
	s.st.AddEvent(run.ID, "status", "run of standing order "+parent.ID+" ("+why+"): "+parent.Goal, map[string]any{"repeat_of": parent.ID})
	s.startAgent(run, "")
	return run, nil
}

// runDueRepeats starts every standing order whose next run is due.
func (s *Server) runDueRepeats(now time.Time) int {
	started := 0
	for _, m := range s.st.ListMissions("") {
		if m.RepeatDays <= 0 || m.NextRunAt == nil || m.NextRunAt.After(now) {
			continue
		}
		if _, err := s.startRun(m, "scheduled"); err != nil {
			log.Printf("standing order %s: %v", m.ID, err)
			continue
		}
		started++
	}
	return started
}

func (s *Server) scheduler() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for now := range t.C {
		s.runDueRepeats(now)
	}
}

func (s *Server) handleRunNow(w http.ResponseWriter, r *http.Request) {
	m, err := s.st.GetMission(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "mission not found")
		return
	}
	run, err := s.startRun(m, "run now")
	if err != nil {
		var he *httpError
		if errors.As(err, &he) {
			writeErr(w, he.code, he.msg)
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, run)
}

func (s *Server) handleStopRepeat(w http.ResponseWriter, r *http.Request) {
	m, err := s.st.GetMission(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "mission not found")
		return
	}
	if m.RepeatDays == 0 {
		writeErr(w, 409, "this mission is not a standing order")
		return
	}
	m, _ = s.st.UpdateMission(m.ID, func(x *store.Mission) { x.RepeatDays, x.NextRunAt = 0, nil })
	s.st.AddEvent(m.ID, "status", "standing order stopped by the human; no further runs", nil)
	writeJSON(w, 200, m)
}

// allowMissionStart enforces the per-hour cap on agent runs (missions and
// rejections both start a run).
func (s *Server) allowMissionStart() bool {
	if s.cfg.MaxMissionsPerHour <= 0 {
		return true
	}
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	cutoff := time.Now().Add(-time.Hour)
	kept := s.rateStarts[:0]
	for _, t := range s.rateStarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.rateStarts = kept
	if len(s.rateStarts) >= s.cfg.MaxMissionsPerHour {
		return false
	}
	s.rateStarts = append(s.rateStarts, time.Now())
	return true
}

// handleApplySuggestion lets a human accept the agent's suggested mandate
// change and retries the goal as a new mission under the amended mandate.
func (s *Server) handleApplySuggestion(w http.ResponseWriter, r *http.Request) {
	ms, err := s.st.GetMission(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "mission not found")
		return
	}
	if ms.Suggestion == nil {
		writeErr(w, 409, "the agent made no suggestion for this mission")
		return
	}
	if ms.Suggestion.AppliedMissionID != "" {
		writeErr(w, 409, "this suggestion was already applied")
		return
	}
	s.payMu.Lock()
	md, err := s.st.GetMandate(ms.MandateID)
	if err != nil {
		s.payMu.Unlock()
		writeErr(w, 404, "mandate not found")
		return
	}
	amended, desc, err := md.Amend(ms.Suggestion.Field, ms.Suggestion.Value)
	if err != nil {
		s.payMu.Unlock()
		writeErr(w, 409, "cannot apply: "+err.Error())
		return
	}
	if !s.allowMissionStart() {
		s.payMu.Unlock()
		writeErr(w, 429, fmt.Sprintf("this demo allows %d agent runs per hour; please try again later", s.cfg.MaxMissionsPerHour))
		return
	}
	_ = s.st.UpdateMandate(md.ID, func(m *mandate.Mandate) error {
		m.TotalBudget, m.MaxPerItem, m.MinRating, m.AutoApproveUnder = amended.TotalBudget, amended.MaxPerItem, amended.MinRating, amended.AutoApproveUnder
		m.AllowedCategories, m.BlockedMerchants = amended.AllowedCategories, amended.BlockedMerchants
		return nil
	})
	s.payMu.Unlock()
	retry := &store.Mission{ID: store.NewID("msn"), MandateID: ms.MandateID, Goal: ms.Goal, Status: store.MissionRunning, Kind: ms.Kind, RetryOf: ms.ID, CreatedAt: time.Now()}
	s.st.PutMission(retry)
	_, _ = s.st.UpdateMission(ms.ID, func(m *store.Mission) { m.Suggestion.AppliedMissionID = retry.ID })
	s.st.AddEvent(ms.ID, "status", "human amended the mandate ("+desc+") and retried the goal as mission "+retry.ID, map[string]any{"retry_mission_id": retry.ID, "change": desc})
	s.st.AddEvent(retry.ID, "status", "retry of mission "+ms.ID+" after the human amended the mandate: "+desc, map[string]any{"retry_of": ms.ID, "change": desc})
	s.startAgent(retry, "")
	writeJSON(w, 201, map[string]any{"mission": retry, "change": desc})
}

func (s *Server) handleGetMission(w http.ResponseWriter, r *http.Request) {
	m, err := s.st.GetMission(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "mission not found")
		return
	}
	md, _ := s.st.GetMandate(m.MandateID)
	var payments []*store.Payment
	for _, p := range s.st.ListPayments() {
		if p.MissionID == m.ID {
			payments = append(payments, p)
		}
	}
	writeJSON(w, 200, map[string]any{
		"mission":   m,
		"mandate":   md,
		"proposals": s.st.ListProposals(m.ID),
		"payments":  payments,
		"events":    s.st.ListEvents(m.ID, 0),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.st.GetMission(id); err != nil {
		writeErr(w, 404, "mission not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	ch, unsub := s.st.Subscribe(id)
	defer unsub()
	send := func(ev store.Event) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, b)
		flusher.Flush()
	}
	for _, ev := range s.st.ListEvents(id, after) {
		send(ev)
		after = ev.ID
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if ev.ID > after {
				send(ev)
				after = ev.ID
			}
		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// --- proposals & payments ---

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	pay, err := s.createPayment(r.Context(), r.PathValue("id"))
	if err != nil {
		var he *httpError
		if errors.As(err, &he) {
			writeErr(w, he.code, he.msg)
			return
		}
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"payment": pay, "approve_url": pay.ApproveURL})
}

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

// createPayment turns an approved proposal into a PayPal order and reserves
// the budget. It is used by the approve endpoint and by auto-approval.
func (s *Server) createPayment(ctx context.Context, proposalID string) (*store.Payment, error) {
	// Phase 1, under payMu: validate, re-check the mandate, reserve the budget
	// and mark the proposal as approved with the order still pending, so a
	// concurrent approval cannot reserve twice.
	s.payMu.Lock()
	p, err := s.st.GetProposal(proposalID)
	if err != nil {
		s.payMu.Unlock()
		return nil, &httpError{404, "proposal not found"}
	}
	if p.Status == store.ProposalApproved {
		if p.OrderID == "" {
			s.payMu.Unlock()
			return nil, &httpError{409, "an order is being created for this proposal; try again in a moment"}
		}
		if pay, err := s.st.GetPayment(p.OrderID); err == nil && !store.IsFinalPayment(pay.Status) {
			s.payMu.Unlock()
			return pay, nil // idempotent: the order already exists and is still payable
		}
	}
	if p.Status != store.ProposalPending && p.Status != store.ProposalAutoApproved {
		s.payMu.Unlock()
		return nil, &httpError{409, "proposal is " + p.Status + ", it cannot be approved"}
	}
	md, err := s.st.GetMandate(p.MandateID)
	if err != nil {
		s.payMu.Unlock()
		return nil, &httpError{404, "mandate not found"}
	}
	// Re-check at approval time: the budget may have moved since the agent proposed.
	d := md.Evaluate(p.Cart)
	if !d.Allowed {
		s.payMu.Unlock()
		s.st.AddEvent(p.MissionID, "policy_block", "approval refused: the cart no longer passes the mandate", map[string]any{"decision": d})
		return nil, &httpError{409, "the cart no longer passes the mandate: " + firstViolation(d)}
	}
	prevStatus := p.Status
	_ = s.st.UpdateMandate(md.ID, func(m *mandate.Mandate) error { m.Reserved = mandate.Round(m.Reserved + d.Total); return nil })
	_, _ = s.st.UpdateProposal(p.ID, func(pr *store.Proposal) {
		pr.Status = store.ProposalApproved
		pr.OrderID = ""
		now := time.Now()
		pr.DecidedAt = &now
	})
	items := make([]paypal.OrderItem, 0, len(p.Cart.Items))
	for _, it := range p.Cart.Items {
		items = append(items, paypal.OrderItem{Name: it.Title, SKU: it.ProductID, Quantity: it.Quantity, UnitAmount: it.UnitPrice, Category: "PHYSICAL_GOODS"})
	}
	req := paypal.CreateOrderRequest{
		Currency:     md.Currency,
		Amount:       d.Total,
		Description:  truncate("Mandate: "+p.Summary, 120),
		CustomID:     p.ID,
		Items:        items,
		ReturnURL:    s.cfg.PublicURL + "/paypal/return",
		CancelURL:    s.cfg.PublicURL + "/paypal/cancel",
		BrandName:    "Mandate",
		StoreInVault: md.SavePaymentMethod && !md.HasSavedPayment(),
		VaultID:      md.VaultID,
	}
	s.payMu.Unlock()

	// Phase 2, without the lock: talk to PayPal. Webhooks, returns and cancels
	// for other orders keep flowing meanwhile.
	undo := func() {
		s.payMu.Lock()
		defer s.payMu.Unlock()
		_ = s.st.UpdateMandate(md.ID, func(m *mandate.Mandate) error {
			m.Reserved = mandate.Round(m.Reserved - d.Total)
			if m.Reserved < 0 {
				m.Reserved = 0
			}
			return nil
		})
		_, _ = s.st.UpdateProposal(p.ID, func(pr *store.Proposal) {
			if pr.Status == store.ProposalApproved && pr.OrderID == "" {
				pr.Status, pr.DecidedAt = prevStatus, nil
			}
		})
	}
	order, err := s.pp.CreateOrder(ctx, req)
	if err != nil && req.VaultID != "" {
		// The saved account could not be charged (token revoked, vault disabled on
		// the merchant account, ...): forget it and fall back to a normal checkout.
		s.payMu.Lock()
		s.forgetSavedPayment(md.ID, p.MissionID, "the saved PayPal account could not be used ("+truncate(err.Error(), 160)+")")
		s.payMu.Unlock()
		req.VaultID = ""
		req.StoreInVault = md.SavePaymentMethod
		req.RequestID = p.ID + "-c"
		order, err = s.pp.CreateOrder(ctx, req)
	}
	if err != nil && req.StoreInVault {
		// Saving payment methods is not enabled for this merchant app: pay without saving.
		s.st.AddEvent(p.MissionID, "payment", "PayPal could not save the payment method ("+truncate(err.Error(), 160)+"); continuing with a regular checkout", nil)
		req.StoreInVault = false
		req.RequestID = p.ID + "-p"
		order, err = s.pp.CreateOrder(ctx, req)
	}
	if err != nil {
		undo()
		s.st.AddEvent(p.MissionID, "error", "PayPal order creation failed: "+err.Error(), nil)
		return nil, fmt.Errorf("PayPal order creation failed: %w", err)
	}
	vaulted := req.VaultID != ""
	var captured *paypal.Order
	var cerr error
	if vaulted && order.Status != store.PaymentCompleted {
		captured, cerr = s.pp.CaptureOrder(ctx, order.ID)
	}

	// Phase 3, under payMu again: bookkeeping.
	s.payMu.Lock()
	defer s.payMu.Unlock()
	pay := &store.Payment{ID: order.ID, ProposalID: p.ID, MissionID: p.MissionID, MandateID: p.MandateID, Status: order.Status, Amount: d.Total, Currency: md.Currency, ApproveURL: order.ApproveURL, Mode: s.pp.Mode(), Reserved: true, Vaulted: vaulted, CreatedAt: time.Now()}
	s.st.PutPayment(pay)
	_, _ = s.st.UpdateProposal(p.ID, func(pr *store.Proposal) {
		pr.Status = store.ProposalApproved
		pr.OrderID = order.ID
	})
	_, _ = s.st.UpdateMission(p.MissionID, func(m *store.Mission) {
		m.Status = store.MissionAwaitingPayment
		m.OrderID = order.ID
	})
	if !vaulted {
		s.st.AddEvent(p.MissionID, "payment", fmt.Sprintf("PayPal order %s created for %.2f %s (%s); waiting for the buyer to approve in PayPal", order.ID, d.Total, md.Currency, s.pp.Mode()), map[string]any{"order_id": order.ID, "approve_url": order.ApproveURL, "status": order.Status})
		return pay, nil
	}

	// Paid with the saved account: no buyer interaction.
	what := "capturing without checkout"
	if order.Status == store.PaymentCompleted {
		what = "captured at creation, no checkout"
	}
	s.st.AddEvent(p.MissionID, "payment", fmt.Sprintf("PayPal order %s created for %.2f %s (%s) with the PayPal account saved on this mandate; %s", order.ID, d.Total, md.Currency, s.pp.Mode(), what), map[string]any{"order_id": order.ID, "status": order.Status, "vaulted": true})
	if cerr != nil {
		// Undo the approval so the human can approve again; the next attempt goes through checkout.
		s.releaseReservation(pay.ID)
		_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = store.PaymentCaptureFailed })
		_, _ = s.st.UpdateProposal(p.ID, func(pr *store.Proposal) { pr.Status = prevStatus; pr.OrderID = ""; pr.DecidedAt = nil })
		_, _ = s.st.UpdateMission(p.MissionID, func(m *store.Mission) {
			m.OrderID = ""
			if prevStatus == store.ProposalPending {
				m.Status = store.MissionAwaitingApproval
			}
		})
		s.forgetSavedPayment(md.ID, p.MissionID, "capture with the saved PayPal account failed ("+truncate(cerr.Error(), 160)+")")
		s.st.AddEvent(p.MissionID, "error", "the saved PayPal account could not be charged; approve again to pay through PayPal checkout", nil)
		return nil, &httpError{502, "the saved PayPal account could not be charged; approve again to pay through PayPal checkout"}
	}
	if captured != nil {
		order = captured
	}
	if order.Status == store.PaymentCompleted {
		s.markPaidLocked(pay.ID, order)
	} else {
		_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = order.Status })
		s.st.AddEvent(p.MissionID, "payment", "PayPal order status: "+order.Status, map[string]any{"order_id": order.ID})
	}
	pay, _ = s.st.GetPayment(pay.ID)
	return pay, nil
}

// forgetSavedPayment removes the saved PayPal account from a mandate after it
// failed to work; the next checkout saves a fresh one if the mandate asks for
// it. Call with payMu held.
func (s *Server) forgetSavedPayment(mandateID, missionID, reason string) {
	md, err := s.st.GetMandate(mandateID)
	if err != nil || !md.HasSavedPayment() {
		return
	}
	_ = s.st.UpdateMandate(mandateID, func(m *mandate.Mandate) error { m.VaultID, m.VaultPayer, m.VaultedAt = "", "", nil; return nil })
	s.st.AddEvent(missionID, "vault", reason+"; the saved account was removed from the mandate and the next checkout will save a new one", map[string]any{"vault_id": md.VaultID})
}

// releaseReservation gives a payment's budget reservation back to the mandate.
// It is a no-op when the reservation was already released. Call with payMu held.
func (s *Server) releaseReservation(payID string) {
	pay, err := s.st.GetPayment(payID)
	if err != nil || !pay.Reserved {
		return
	}
	_, _ = s.st.UpdatePayment(payID, func(x *store.Payment) { x.Reserved = false })
	_ = s.st.UpdateMandate(pay.MandateID, func(m *mandate.Mandate) error {
		m.Reserved = mandate.Round(m.Reserved - pay.Amount)
		if m.Reserved < 0 {
			m.Reserved = 0
		}
		return nil
	})
}

// voidPayment records that an order will never be captured (buyer cancelled,
// order expired, human withdrew approval) and releases its reservation.
// Call with payMu held. Returns false if the payment is already final.
func (s *Server) voidPayment(payID, status, event, message string) bool {
	pay, err := s.st.GetPayment(payID)
	if err != nil || store.IsFinalPayment(pay.Status) {
		return false
	}
	s.releaseReservation(payID)
	_, _ = s.st.UpdatePayment(payID, func(x *store.Payment) { x.Status = status })
	_, _ = s.st.UpdateProposal(pay.ProposalID, func(pr *store.Proposal) {
		if pr.Status == store.ProposalApproved || pr.Status == store.ProposalAutoApproved || pr.Status == store.ProposalPending {
			pr.Status = store.ProposalCancelled
		}
	})
	_, _ = s.st.UpdateMission(pay.MissionID, func(m *store.Mission) {
		if m.Status == store.MissionAwaitingPayment || m.Status == store.MissionAwaitingApproval {
			m.Status = store.MissionCancelled
		}
	})
	s.st.AddEvent(pay.MissionID, event, message, map[string]any{"order_id": payID, "status": status})
	s.shopper.Forget(pay.MissionID)
	return true
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	_ = readJSON(r, &in)
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" {
		in.Reason = "rejected without a reason"
	}
	s.payMu.Lock()
	p, err := s.st.GetProposal(r.PathValue("id"))
	if err != nil {
		s.payMu.Unlock()
		writeErr(w, 404, "proposal not found")
		return
	}
	switch p.Status {
	case store.ProposalPending, store.ProposalAutoApproved:
	case store.ProposalApproved:
		if p.OrderID == "" {
			s.payMu.Unlock()
			writeErr(w, 409, "an order is being created for this proposal; try again in a moment")
			return
		}
		if pay, err := s.st.GetPayment(p.OrderID); err != nil || pay.Status == store.PaymentCompleted || pay.Status == store.PaymentPartiallyRefunded {
			s.payMu.Unlock()
			writeErr(w, 409, "this proposal has already been paid")
			return
		}
	default:
		s.payMu.Unlock()
		writeErr(w, 409, "proposal is "+p.Status+", it cannot be rejected")
		return
	}
	// The rate limit is checked before anything changes, so a refusal leaves
	// the proposal exactly as it was.
	if !s.allowMissionStart() {
		s.payMu.Unlock()
		writeErr(w, 429, fmt.Sprintf("this demo allows %d agent runs per hour; please try again later", s.cfg.MaxMissionsPerHour))
		return
	}
	if p.Status == store.ProposalApproved {
		// An order exists but has not been captured: withdraw it. The return
		// redirect and webhooks refuse to capture a withdrawn order.
		s.releaseReservation(p.OrderID)
		_, _ = s.st.UpdatePayment(p.OrderID, func(x *store.Payment) { x.Status = store.PaymentCancelled })
		s.st.AddEvent(p.MissionID, "cancelled", "order "+p.OrderID+" withdrawn before capture; reservation released", map[string]any{"order_id": p.OrderID})
	}
	now := time.Now()
	_, _ = s.st.UpdateProposal(p.ID, func(pr *store.Proposal) {
		pr.Status = store.ProposalRejected
		pr.Feedback = in.Reason
		pr.DecidedAt = &now
	})
	_, _ = s.st.UpdateMission(p.MissionID, func(m *store.Mission) {
		m.Status = store.MissionRunning
		m.ProposalID = ""
		m.OrderID = ""
	})
	s.payMu.Unlock()
	s.st.AddEvent(p.MissionID, "rejected", "human rejected the proposal: "+in.Reason, map[string]any{"proposal_id": p.ID})
	if ms, err := s.st.GetMission(p.MissionID); err == nil && ms.Kind == "negotiate" {
		go s.shopper.Negotiate(context.Background(), p.MissionID, in.Reason)
	} else {
		go s.shopper.Resume(context.Background(), p.MissionID, in.Reason)
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleSyncPayment re-reads an order from PayPal. If the buyer approved it
// but the return redirect never reached us (closed tab, webhook-less demo), the
// order is captured here; if PayPal already shows it completed, our ledger is
// reconciled. This is what a merchant would normally do from a webhook.
func (s *Server) handleSyncPayment(w http.ResponseWriter, r *http.Request) {
	pay, err := s.st.GetPayment(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "payment not found")
		return
	}
	if pay.Status == store.PaymentCompleted || pay.Status == store.PaymentPartiallyRefunded {
		// Finish anything PayPal completed that we never accounted for (a
		// restart mid-capture), and pick up an account saved asynchronously
		// after the capture. Otherwise there is nothing to ask PayPal.
		wantVault := false
		if md, err := s.st.GetMandate(pay.MandateID); err == nil && md.SavePaymentMethod && !md.HasSavedPayment() {
			wantVault = true
		}
		if pay.Accounted && !wantVault {
			writeJSON(w, 200, pay)
			return
		}
		order, err := s.pp.GetOrder(r.Context(), pay.ID)
		if err != nil {
			writeErr(w, 502, "PayPal lookup failed: "+err.Error())
			return
		}
		s.payMu.Lock()
		if order.Status == store.PaymentCompleted {
			s.markPaidLocked(pay.ID, order)
		}
		s.adoptVaultLocked(pay, order.VaultID, order.VaultStatus, order.PayerEmail)
		s.payMu.Unlock()
		pay, _ = s.st.GetPayment(pay.ID)
		writeJSON(w, 200, pay)
		return
	}
	if store.IsFinalPayment(pay.Status) {
		writeJSON(w, 200, pay) // given up or refunded: nothing to sync
		return
	}
	order, err := s.pp.GetOrder(r.Context(), pay.ID)
	if err != nil {
		writeErr(w, 502, "PayPal lookup failed: "+err.Error())
		return
	}
	if order.Status == "APPROVED" {
		if cap, err := s.pp.CaptureOrder(r.Context(), pay.ID); err == nil {
			order = cap
		} else {
			writeErr(w, 502, "PayPal capture failed: "+err.Error())
			return
		}
	}
	switch order.Status {
	case store.PaymentCompleted:
		s.markPaid(pay.ID, order)
	case store.PaymentVoided, store.PaymentExpired:
		s.payMu.Lock()
		s.voidPayment(pay.ID, order.Status, "cancelled", "PayPal reports the order is "+strings.ToLower(order.Status)+"; reservation released")
		s.payMu.Unlock()
	default:
		s.payMu.Lock()
		if cur, err := s.st.GetPayment(pay.ID); err == nil && !store.IsFinalPayment(cur.Status) {
			_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = order.Status })
		}
		s.payMu.Unlock()
	}
	pay, _ = s.st.GetPayment(pay.ID)
	writeJSON(w, 200, pay)
}

// markPaid records a completed capture everywhere it matters. It is
// idempotent and serialised: the return redirect, the sync endpoint and a
// webhook can all report the same capture without double-counting.
func (s *Server) markPaid(payID string, order *paypal.Order) {
	s.payMu.Lock()
	defer s.payMu.Unlock()
	s.markPaidLocked(payID, order)
}

// markPaidLocked is markPaid for callers that already hold payMu. It books a
// completed capture exactly once (Payment.Accounted is the key): the
// reservation is released, the amount moves to spent, and the proposal and
// mission become paid when this is still their current order. A capture of
// an order the proposal has since moved on from (PayPal charged it after we
// had given it up) is still booked, because the money moved, and flagged for
// a refund.
func (s *Server) markPaidLocked(payID string, order *paypal.Order) {
	pay, err := s.st.GetPayment(payID)
	if err != nil || pay.Accounted {
		return
	}
	if store.IsFinalPayment(pay.Status) && pay.Status != store.PaymentCompleted {
		return // cancelled, voided, expired, refunded or denied: never resurrected
	}
	if order == nil || order.Status != store.PaymentCompleted {
		return
	}
	amount := order.Amount
	if amount == 0 {
		amount = pay.Amount
	}
	s.releaseReservation(payID)
	_, _ = s.st.UpdatePayment(payID, func(x *store.Payment) {
		x.Status = store.PaymentCompleted
		x.CaptureID = order.CaptureID
		x.PayerEmail = order.PayerEmail
		x.Amount = amount
		x.Accounted = true
	})
	_ = s.st.UpdateMandate(pay.MandateID, func(m *mandate.Mandate) error {
		m.Spent = mandate.Round(m.Spent + amount)
		return nil
	})
	current := true
	if pr, err := s.st.GetProposal(pay.ProposalID); err == nil && pr.OrderID != "" && pr.OrderID != payID {
		current = false
	}
	if !current {
		s.st.AddEvent(pay.MissionID, "error", fmt.Sprintf("PayPal captured order %s (%.2f %s) after this proposal had moved on to another order; the amount is counted as spent and should be refunded", payID, amount, pay.Currency), map[string]any{"order_id": payID, "capture_id": order.CaptureID, "amount": amount})
		return
	}
	_, _ = s.st.UpdateProposal(pay.ProposalID, func(pr *store.Proposal) { pr.Status = store.ProposalPaid })
	_, _ = s.st.UpdateMission(pay.MissionID, func(m *store.Mission) { m.Status = store.MissionPaid })
	how := ""
	if pay.Vaulted {
		how = " with the saved PayPal account, no checkout needed"
	}
	s.st.AddEvent(pay.MissionID, "paid", fmt.Sprintf("payment captured: %.2f %s (capture %s)%s", amount, pay.Currency, order.CaptureID, how), map[string]any{"order_id": order.ID, "capture_id": order.CaptureID, "amount": amount, "vaulted": pay.Vaulted})
	s.shopper.Forget(pay.MissionID)

	// First vaulted checkout under a mandate that asked to save the account.
	s.adoptVaultLocked(pay, order.VaultID, order.VaultStatus, order.PayerEmail)
}

// adoptVaultLocked stores a saved PayPal account on the payment's mandate
// when the mandate asked for one and has none yet. PayPal reports the token
// on the capture response (status VAULTED) or, when saving completes
// asynchronously, through the VAULT.PAYMENT-TOKEN.CREATED webhook. Call with
// payMu held.
func (s *Server) adoptVaultLocked(pay *store.Payment, vaultID, vaultStatus, payer string) {
	if vaultID == "" || (vaultStatus != "" && vaultStatus != "VAULTED") {
		return
	}
	md, err := s.st.GetMandate(pay.MandateID)
	if err != nil || !md.SavePaymentMethod || md.HasSavedPayment() {
		return
	}
	now := time.Now()
	_ = s.st.UpdateMandate(md.ID, func(m *mandate.Mandate) error {
		m.VaultID, m.VaultPayer, m.VaultedAt = vaultID, payer, &now
		return nil
	})
	msg := "PayPal account saved on this mandate: approved carts now pay with one click"
	if md.AutoApproveUnder > 0 && !md.RequireApproval {
		msg += fmt.Sprintf(", and carts under %.2f %s pay with no click at all", md.AutoApproveUnder, md.Currency)
	}
	s.st.AddEvent(pay.MissionID, "vault", msg, map[string]any{"vault_id": vaultID, "payer": payer})
}

// markRefunded applies a refund, reversal or denial. A partial refund gives
// that amount back to the mandate and leaves the proposal paid; a full
// refund (or reversal) marks proposal and mission refunded. A denial of a
// capture that never completed is a void: the reservation is released.
// Repeated deliveries never credit more than was captured.
func (s *Server) markRefunded(payID, status string, amount float64) {
	s.payMu.Lock()
	defer s.payMu.Unlock()
	pay, err := s.st.GetPayment(payID)
	if err != nil {
		return
	}
	if pay.Status != store.PaymentCompleted && pay.Status != store.PaymentPartiallyRefunded {
		if status == store.PaymentDenied && !store.IsFinalPayment(pay.Status) {
			s.voidPayment(payID, store.PaymentDenied, "cancelled", "PayPal denied the capture; reservation released")
		}
		return
	}
	remaining := mandate.Round(pay.Amount - pay.Refunded)
	credit := remaining
	if amount > 0 && amount < remaining-0.005 {
		credit = mandate.Round(amount)
	}
	if credit <= 0 {
		return
	}
	full := credit >= remaining-0.005
	_ = s.st.UpdateMandate(pay.MandateID, func(m *mandate.Mandate) error {
		m.Spent = mandate.Round(m.Spent - credit)
		if m.Spent < 0 {
			m.Spent = 0
		}
		return nil
	})
	_, _ = s.st.UpdatePayment(payID, func(x *store.Payment) {
		x.Refunded = mandate.Round(x.Refunded + credit)
		if full {
			x.Status = status
		} else {
			x.Status = store.PaymentPartiallyRefunded
		}
	})
	if full {
		_, _ = s.st.UpdateProposal(pay.ProposalID, func(pr *store.Proposal) { pr.Status = store.ProposalRefunded })
		_, _ = s.st.UpdateMission(pay.MissionID, func(m *store.Mission) { m.Status = store.MissionRefunded })
		s.st.AddEvent(pay.MissionID, "refunded", fmt.Sprintf("capture %s: %.2f %s returned to the mandate", status, credit, pay.Currency), map[string]any{"order_id": payID, "status": status, "amount": credit})
		return
	}
	s.st.AddEvent(pay.MissionID, "refunded", fmt.Sprintf("partial refund: %.2f %s of %.2f returned to the mandate", credit, pay.Currency, pay.Amount), map[string]any{"order_id": payID, "status": status, "amount": credit})
}

func (s *Server) handleLedger(w http.ResponseWriter, _ *http.Request) {
	type row struct {
		*store.Payment
		Goal        string  `json:"goal"`
		MandateName string  `json:"mandate_name"`
		Items       int     `json:"items"`
		Budget      float64 `json:"budget"`
	}
	var rows []row
	for _, p := range s.st.ListPayments() {
		r := row{Payment: p}
		if m, err := s.st.GetMission(p.MissionID); err == nil {
			r.Goal = m.Goal
		}
		if md, err := s.st.GetMandate(p.MandateID); err == nil {
			r.MandateName = md.Name
			r.Budget = md.TotalBudget
		}
		if pr, err := s.st.GetProposal(p.ProposalID); err == nil {
			r.Items = len(pr.Cart.Items)
		}
		rows = append(rows, r)
	}
	if rows == nil {
		rows = []row{}
	}
	writeJSON(w, 200, rows)
}

// --- PayPal redirects ---

func (s *Server) handlePayPalReturn(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	pay, err := s.st.GetPayment(token)
	if err != nil {
		http.Redirect(w, r, "/#/?error=unknown_order", http.StatusFound)
		return
	}
	if pay.Status == store.PaymentCompleted || pay.Status == store.PaymentPartiallyRefunded { // refreshed return page
		if !pay.Accounted {
			if order, err := s.pp.GetOrder(r.Context(), pay.ID); err == nil {
				s.markPaid(pay.ID, order)
			}
		}
		http.Redirect(w, r, "/#/missions/"+pay.MissionID+"?payment=completed", http.StatusFound)
		return
	}
	if store.IsFinalPayment(pay.Status) {
		// Withdrawn by the approver, voided or expired: the buyer's approval
		// at PayPal must not turn into a capture.
		s.st.AddEvent(pay.MissionID, "cancelled", "buyer returned from PayPal for order "+pay.ID+" but it was already "+strings.ToLower(pay.Status)+"; not captured", map[string]any{"order_id": pay.ID})
		http.Redirect(w, r, "/#/missions/"+pay.MissionID+"?payment=cancelled", http.StatusFound)
		return
	}
	order, err := s.pp.CaptureOrder(r.Context(), token)
	if err != nil {
		s.st.AddEvent(pay.MissionID, "error", "PayPal capture failed: "+err.Error(), nil)
		s.payMu.Lock()
		if cur, err := s.st.GetPayment(pay.ID); err == nil && !store.IsFinalPayment(cur.Status) {
			_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = store.PaymentCaptureFailed })
		}
		s.payMu.Unlock()
		http.Redirect(w, r, "/#/missions/"+pay.MissionID+"?payment=failed", http.StatusFound)
		return
	}
	if order.Status == store.PaymentCompleted {
		s.markPaid(pay.ID, order)
	} else {
		s.payMu.Lock()
		if cur, err := s.st.GetPayment(pay.ID); err == nil && !store.IsFinalPayment(cur.Status) {
			_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = order.Status })
		}
		s.payMu.Unlock()
		s.st.AddEvent(pay.MissionID, "payment", "PayPal order status: "+order.Status, map[string]any{"order_id": order.ID})
	}
	http.Redirect(w, r, "/#/missions/"+pay.MissionID+"?payment="+strings.ToLower(order.Status), http.StatusFound)
}

func (s *Server) handlePayPalCancel(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	pay, err := s.st.GetPayment(token)
	if err != nil {
		http.Redirect(w, r, "/#/", http.StatusFound)
		return
	}
	s.payMu.Lock()
	s.voidPayment(pay.ID, "CANCELLED", "cancelled", "buyer cancelled at PayPal checkout; reservation released")
	s.payMu.Unlock()
	http.Redirect(w, r, "/#/missions/"+pay.MissionID+"?payment=cancelled", http.StatusFound)
}

// handlePayPalWebhook receives PayPal webhook deliveries. Every delivery is
// verified with PayPal before it is trusted. CHECKOUT.ORDER.APPROVED captures
// the order (the buyer approved but may never have reached the return URL);
// PAYMENT.CAPTURE.COMPLETED reconciles the ledger if the capture happened
// elsewhere. Unknown orders and other event types are acknowledged and ignored
// so PayPal does not retry them.
func (s *Server) handlePayPalWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, "unreadable body")
		return
	}
	ok, err := s.pp.VerifyWebhook(r.Context(), s.cfg.WebhookID, r.Header, body)
	if err != nil {
		log.Printf("webhook verification error: %v", err)
		writeErr(w, 502, "verification failed")
		return
	}
	if !ok {
		writeErr(w, 401, "invalid webhook signature")
		return
	}
	var ev paypal.WebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		writeErr(w, 400, "invalid event")
		return
	}
	orderID := ev.OrderID()
	pay, perr := s.st.GetPayment(orderID)
	if orderID == "" || perr != nil {
		writeJSON(w, 200, map[string]any{"ok": true, "ignored": "unknown order"})
		return
	}
	s.st.AddEvent(pay.MissionID, "webhook", "PayPal webhook "+ev.EventType+" for order "+orderID, map[string]any{"event_id": ev.ID, "event_type": ev.EventType})
	switch ev.EventType {
	case "CHECKOUT.ORDER.APPROVED":
		if !store.IsFinalPayment(pay.Status) { // never capture a withdrawn, voided or expired order
			order, err := s.pp.CaptureOrder(r.Context(), orderID)
			if err != nil {
				s.st.AddEvent(pay.MissionID, "error", "capture after webhook failed: "+err.Error(), nil)
				writeErr(w, 502, "capture failed")
				return
			}
			if order.Status == store.PaymentCompleted {
				s.markPaid(pay.ID, order)
			}
		}
	case "PAYMENT.CAPTURE.COMPLETED":
		if !store.IsFinalPayment(pay.Status) || (pay.Status == store.PaymentCompleted && !pay.Accounted) {
			order, err := s.pp.GetOrder(r.Context(), orderID)
			if err == nil && order.Status == store.PaymentCompleted {
				s.markPaid(pay.ID, order)
			}
		}
	case "PAYMENT.CAPTURE.REFUNDED", "PAYMENT.CAPTURE.REVERSED", "PAYMENT.CAPTURE.DENIED":
		amount, _ := strconv.ParseFloat(ev.Resource.Amount.Value, 64)
		s.markRefunded(pay.ID, strings.TrimPrefix(ev.EventType, "PAYMENT.CAPTURE."), amount)
	case "CHECKOUT.ORDER.VOIDED":
		s.payMu.Lock()
		s.voidPayment(pay.ID, "VOIDED", "cancelled", "PayPal voided the order; reservation released")
		s.payMu.Unlock()
	case "VAULT.PAYMENT-TOKEN.CREATED":
		s.payMu.Lock()
		s.adoptVaultLocked(pay, ev.Resource.ID, "VAULTED", ev.Resource.PaymentSource.PayPal.EmailAddress)
		s.payMu.Unlock()
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- mock PayPal approval simulator ---

var mockPage = template.Must(template.New("mock").Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Sandbox approval simulator</title>
<style>body{font-family:system-ui,sans-serif;background:#f3f4f6;margin:0;padding:40px}main{max-width:520px;margin:0 auto;background:#fff;border-radius:12px;padding:28px;box-shadow:0 6px 30px rgba(0,0,0,.08)}h1{font-size:20px;margin:0 0 4px}p.sub{color:#6b7280;margin:0 0 20px}table{width:100%;border-collapse:collapse;margin:12px 0}td{padding:6px 0;border-bottom:1px solid #eee}td.r{text-align:right}tr.total td{font-weight:700;border:0}button{padding:12px 18px;border:0;border-radius:999px;font-size:15px;cursor:pointer}button.pay{background:#0070ba;color:#fff;width:100%}button.cancel{background:transparent;color:#374151;width:100%;margin-top:8px}.badge{display:inline-block;background:#fef3c7;color:#92400e;padding:3px 8px;border-radius:6px;font-size:12px;margin-bottom:12px}</style></head>
<body><main><span class="badge">MOCK MODE: no real PayPal call is made</span><h1>Pay with PayPal (simulated)</h1><p class="sub">Order {{.Order.ID}}</p>
<table>{{range .Items}}<tr><td>{{.Name}} × {{.Quantity}}</td><td class="r">{{printf "%.2f" .UnitAmount}}</td></tr>{{end}}<tr class="total"><td>Total</td><td class="r">{{printf "%.2f" .Order.Amount}} {{.Order.Currency}}</td></tr></table>
<form method="post" action="/mock/paypal/approve"><input type="hidden" name="token" value="{{.Order.ID}}"><button class="pay" name="action" value="approve">Approve and pay</button><button class="cancel" name="action" value="cancel">Cancel and return</button></form>
<p class="sub" style="margin-top:16px;font-size:12px">In sandbox mode this page is replaced by the real PayPal checkout, where the buyer logs in with a sandbox personal account.</p></main></body></html>`))

func (s *Server) handleMockApprovePage(w http.ResponseWriter, r *http.Request) {
	order, items, err := s.ppMock.Details(r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = mockPage.Execute(w, map[string]any{"Order": order, "Items": items})
}

func (s *Server) handleMockApprove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	next, err := s.ppMock.Approve(r.Form.Get("token"), r.Form.Get("action") == "approve")
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// --- static UI ---

func (s *Server) staticHandler() http.Handler {
	dir := s.cfg.StaticDir
	index := filepath.Join(dir, "index.html")
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, 404, "not found")
			return
		}
		if _, err := os.Stat(index); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<h1>Mandate API is running</h1><p>The web UI is not built. Run <code>cd web && npm install && npm run build</code>, restart the server, or use the API at <code>/api/*</code>.</p>")
			return
		}
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, index)
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstViolation(d mandate.Decision) string {
	if len(d.Violations) == 0 {
		return ""
	}
	return d.Violations[0].Message
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/paypal/") {
			log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

// QueryEscape is exported for templates/tests that build redirect URLs.
func QueryEscape(s string) string { return url.QueryEscape(s) }
