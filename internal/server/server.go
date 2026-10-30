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
	s.routes()
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

	m.HandleFunc("GET /api/missions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.st.ListMissions(r.URL.Query().Get("mandate_id")))
	})
	m.HandleFunc("POST /api/missions", s.handleCreateMission)
	m.HandleFunc("GET /api/missions/{id}", s.handleGetMission)
	m.HandleFunc("GET /api/missions/{id}/events", s.handleEvents)

	m.HandleFunc("POST /api/proposals/{id}/approve", s.handleApprove)
	m.HandleFunc("POST /api/proposals/{id}/reject", s.handleReject)
	m.HandleFunc("GET /api/ledger", s.handleLedger)
	m.HandleFunc("POST /api/payments/{id}/sync", s.handleSyncPayment)

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
		"paypal_mode":  s.pp.Mode(),
		"llm_provider": s.shopper.Provider.Name(),
		"catalog":      s.catalog.Name(),
		"currency":     s.cfg.Currency,
		"public_url":   s.cfg.PublicURL,
		"version":      s.cfg.Version,
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	maxPrice, _ := strconv.ParseFloat(q.Get("max_price"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	res, err := s.catalog.Search(r.Context(), catalog.Query{Text: q.Get("q"), MaxPrice: maxPrice, Category: q.Get("category"), Limit: limit})
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
		MandateID string `json:"mandate_id"`
		Goal      string `json:"goal"`
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
	m := &store.Mission{ID: store.NewID("msn"), MandateID: in.MandateID, Goal: in.Goal, Status: store.MissionRunning, CreatedAt: time.Now()}
	s.st.PutMission(m)
	s.st.AddEvent(m.ID, "status", "mission created: "+in.Goal, nil)
	go s.shopper.Run(context.Background(), m.ID)
	writeJSON(w, 201, m)
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
	p, err := s.st.GetProposal(proposalID)
	if err != nil {
		return nil, &httpError{404, "proposal not found"}
	}
	if p.Status == store.ProposalApproved && p.OrderID != "" {
		if pay, err := s.st.GetPayment(p.OrderID); err == nil && pay.Status != "CANCELLED" {
			return pay, nil // idempotent: the order already exists and is still payable
		}
	}
	if p.Status != store.ProposalPending && p.Status != store.ProposalAutoApproved {
		return nil, &httpError{409, "proposal is " + p.Status + ", it cannot be approved"}
	}
	md, err := s.st.GetMandate(p.MandateID)
	if err != nil {
		return nil, &httpError{404, "mandate not found"}
	}
	// Re-check at approval time: the budget may have moved since the agent proposed.
	d := md.Evaluate(p.Cart)
	if !d.Allowed {
		s.st.AddEvent(p.MissionID, "policy_block", "approval refused: the cart no longer passes the mandate", map[string]any{"decision": d})
		return nil, &httpError{409, "the cart no longer passes the mandate: " + firstViolation(d)}
	}
	items := make([]paypal.OrderItem, 0, len(p.Cart.Items))
	for _, it := range p.Cart.Items {
		items = append(items, paypal.OrderItem{Name: it.Title, SKU: it.ProductID, Quantity: it.Quantity, UnitAmount: it.UnitPrice, Category: "PHYSICAL_GOODS"})
	}
	order, err := s.pp.CreateOrder(ctx, paypal.CreateOrderRequest{
		Currency:    md.Currency,
		Amount:      d.Total,
		Description: truncate("Mandate: "+p.Summary, 120),
		CustomID:    p.ID,
		Items:       items,
		ReturnURL:   s.cfg.PublicURL + "/paypal/return",
		CancelURL:   s.cfg.PublicURL + "/paypal/cancel",
		BrandName:   "Mandate",
	})
	if err != nil {
		s.st.AddEvent(p.MissionID, "error", "PayPal order creation failed: "+err.Error(), nil)
		return nil, fmt.Errorf("PayPal order creation failed: %w", err)
	}
	pay := &store.Payment{ID: order.ID, ProposalID: p.ID, MissionID: p.MissionID, MandateID: p.MandateID, Status: order.Status, Amount: d.Total, Currency: md.Currency, ApproveURL: order.ApproveURL, Mode: s.pp.Mode(), CreatedAt: time.Now()}
	s.st.PutPayment(pay)
	_ = s.st.UpdateMandate(md.ID, func(m *mandate.Mandate) error { m.Reserved = mandate.Round(m.Reserved + d.Total); return nil })
	_, _ = s.st.UpdateProposal(p.ID, func(pr *store.Proposal) {
		pr.Status = store.ProposalApproved
		pr.OrderID = order.ID
		now := time.Now()
		pr.DecidedAt = &now
	})
	_, _ = s.st.UpdateMission(p.MissionID, func(m *store.Mission) {
		m.Status = store.MissionAwaitingPayment
		m.OrderID = order.ID
	})
	s.st.AddEvent(p.MissionID, "payment", fmt.Sprintf("PayPal order %s created for %.2f %s (%s); waiting for the buyer to approve in PayPal", order.ID, d.Total, md.Currency, s.pp.Mode()), map[string]any{"order_id": order.ID, "approve_url": order.ApproveURL, "status": order.Status})
	return pay, nil
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
	p, err := s.st.GetProposal(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "proposal not found")
		return
	}
	if p.Status != store.ProposalPending && p.Status != store.ProposalAutoApproved {
		writeErr(w, 409, "proposal is "+p.Status+", it cannot be rejected")
		return
	}
	if !s.allowMissionStart() {
		writeErr(w, 429, fmt.Sprintf("this demo allows %d agent runs per hour; please try again later", s.cfg.MaxMissionsPerHour))
		return
	}
	if p.OrderID != "" {
		// an auto-approved proposal already has an order: release the reservation and void it locally
		if pay, err := s.st.GetPayment(p.OrderID); err == nil && pay.Status != "COMPLETED" {
			_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = "CANCELLED" })
			_ = s.st.UpdateMandate(p.MandateID, func(m *mandate.Mandate) error { m.Reserved = mandate.Round(m.Reserved - pay.Amount); return nil })
		}
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
	s.st.AddEvent(p.MissionID, "rejected", "human rejected the proposal: "+in.Reason, map[string]any{"proposal_id": p.ID})
	go s.shopper.Resume(context.Background(), p.MissionID, in.Reason)
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
	if pay.Status == "COMPLETED" || pay.Status == "CANCELLED" {
		writeJSON(w, 200, pay)
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
	if order.Status == "COMPLETED" {
		s.markPaid(pay, order)
	} else {
		pay, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = order.Status })
	}
	pay, _ = s.st.GetPayment(pay.ID)
	writeJSON(w, 200, pay)
}

// markPaid records a completed capture everywhere it matters. It is
// idempotent: the return redirect, the sync endpoint and a webhook can all
// report the same capture.
func (s *Server) markPaid(pay *store.Payment, order *paypal.Order) {
	if cur, err := s.st.GetPayment(pay.ID); err == nil && cur.Status == "COMPLETED" {
		return
	}
	amount := order.Amount
	if amount == 0 {
		amount = pay.Amount
	}
	_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) {
		x.Status = order.Status
		x.CaptureID = order.CaptureID
		x.PayerEmail = order.PayerEmail
	})
	_ = s.st.UpdateMandate(pay.MandateID, func(m *mandate.Mandate) error {
		m.Reserved = mandate.Round(m.Reserved - pay.Amount)
		m.Spent = mandate.Round(m.Spent + amount)
		return nil
	})
	_, _ = s.st.UpdateProposal(pay.ProposalID, func(pr *store.Proposal) { pr.Status = store.ProposalPaid })
	_, _ = s.st.UpdateMission(pay.MissionID, func(m *store.Mission) { m.Status = store.MissionPaid })
	s.st.AddEvent(pay.MissionID, "paid", fmt.Sprintf("payment captured: %.2f %s (capture %s)", amount, pay.Currency, order.CaptureID), map[string]any{"order_id": order.ID, "capture_id": order.CaptureID, "amount": amount})
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
	order, err := s.pp.CaptureOrder(r.Context(), token)
	if err != nil {
		s.st.AddEvent(pay.MissionID, "error", "PayPal capture failed: "+err.Error(), nil)
		_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = "CAPTURE_FAILED" })
		http.Redirect(w, r, "/#/missions/"+pay.MissionID+"?payment=failed", http.StatusFound)
		return
	}
	if order.Status == "COMPLETED" {
		s.markPaid(pay, order)
	} else {
		_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = order.Status })
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
	_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = "CANCELLED" })
	_ = s.st.UpdateMandate(pay.MandateID, func(m *mandate.Mandate) error { m.Reserved = mandate.Round(m.Reserved - pay.Amount); return nil })
	_, _ = s.st.UpdateProposal(pay.ProposalID, func(pr *store.Proposal) { pr.Status = store.ProposalCancelled })
	_, _ = s.st.UpdateMission(pay.MissionID, func(m *store.Mission) { m.Status = store.MissionCancelled })
	s.st.AddEvent(pay.MissionID, "cancelled", "buyer cancelled at PayPal checkout; reservation released", map[string]any{"order_id": pay.ID})
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
		if pay.Status != "COMPLETED" {
			order, err := s.pp.CaptureOrder(r.Context(), orderID)
			if err != nil {
				s.st.AddEvent(pay.MissionID, "error", "capture after webhook failed: "+err.Error(), nil)
				writeErr(w, 502, "capture failed")
				return
			}
			if order.Status == "COMPLETED" {
				s.markPaid(pay, order)
			}
		}
	case "PAYMENT.CAPTURE.COMPLETED":
		if pay.Status != "COMPLETED" {
			order, err := s.pp.GetOrder(r.Context(), orderID)
			if err == nil && order.Status == "COMPLETED" {
				s.markPaid(pay, order)
			}
		}
	case "PAYMENT.CAPTURE.DENIED", "PAYMENT.CAPTURE.REFUNDED", "PAYMENT.CAPTURE.REVERSED":
		_, _ = s.st.UpdatePayment(pay.ID, func(x *store.Payment) { x.Status = strings.TrimPrefix(ev.EventType, "PAYMENT.CAPTURE.") })
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
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
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
