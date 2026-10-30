// Package agent runs the shopping agent: a Claude-powered tool loop whose only
// ways to act are the tools defined here. The agent can search a catalog,
// check a cart against the mandate and propose a cart. It cannot pay: payment
// happens only after a human approves the proposal (see internal/server).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"mandate/internal/catalog"
	"mandate/internal/mandate"
	"mandate/internal/store"
)

// Event is a progress message emitted while the agent works.
type Event struct {
	Type    string // assistant | tool_call | tool_result | proposal | error | done
	Message string
	Data    any
}

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
	Run         func(ctx context.Context, input json.RawMessage) (string, error)
}

// Session is one conversation with the model.
type Session interface {
	// Send adds a user message and runs the tool loop until the model stops.
	// It returns the model's final text.
	Send(ctx context.Context, userText string, tools []Tool, emit func(Event)) (string, error)
}

// Provider creates sessions.
type Provider interface {
	Name() string
	NewSession(system string) Session
}

// Shopper wires the provider, catalog and store together.
type Shopper struct {
	Provider Provider
	Catalog  catalog.Searcher
	Store    *store.Store
	// OnProposal is called after a proposal is stored. The server uses it to
	// create a PayPal order immediately for auto-approved proposals.
	OnProposal func(ctx context.Context, p *store.Proposal) error
	// MaxSearches caps catalog calls per mission turn (defence against loops).
	MaxSearches int

	mu       sync.Mutex
	sessions map[string]Session
	order    []string // mission IDs in creation order, for eviction
	runLocks map[string]*sync.Mutex
}

// maxSessions bounds the conversations kept in memory on a long-running server.
const maxSessions = 200

// NewShopper builds a Shopper.
func NewShopper(p Provider, c catalog.Searcher, st *store.Store) *Shopper {
	return &Shopper{Provider: p, Catalog: c, Store: st, MaxSearches: 8, sessions: map[string]Session{}, runLocks: map[string]*sync.Mutex{}}
}

// Forget drops the conversation for a mission that can no longer be resumed
// (paid, cancelled, refunded). It is safe to call for unknown missions.
func (s *Shopper) Forget(missionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, missionID)
	delete(s.runLocks, missionID)
}

// runLock serialises agent runs per mission: a rejection arriving while the
// model is still finishing its previous turn waits instead of racing it.
func (s *Shopper) runLock(missionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.runLocks[missionID]
	if !ok {
		l = &sync.Mutex{}
		s.runLocks[missionID] = l
	}
	return l
}

// SystemPrompt is what the model is told it is.
const SystemPrompt = `You are Mandate, a careful personal shopping agent. A human has delegated a purchase to you under a MANDATE: a budget, a per-item cap, allowed categories, blocked merchants and a minimum rating. The mandate is law. You research, compare and propose; a human (and PayPal checkout) decides whether money moves.

How to work:
1. Read the goal and the mandate. Search for each distinct item the goal asks for with specific queries (product type + the attribute that matters, e.g. "mechanical keyboard brown switches"). Refine the query if results are poor. Do not search more than a handful of times.
2. Compare candidates on how well they meet every stated requirement, rating and review count, and price. Prefer the best value, not the most expensive option the budget allows.
3. Before proposing, call check_mandate with the exact cart. If it reports violations, fix the cart (cheaper item, different merchant, drop an optional item) and check again.
4. Call propose_cart exactly once with the final cart, one short factual reason per item and a one-paragraph summary. Quantity is 1 unless the goal says otherwise.
5. If the goal cannot be met within the mandate, do not propose. Explain precisely what blocks it (for example "the only 4K USB-C monitors cost at least $329, above the $250 per-item cap") so the human can adjust the mandate.
6. After propose_cart returns pending_approval, auto_approved or paid, stop calling tools and write a 2-4 sentence summary for the human: what you chose, why, the total, and what you considered and rejected. If the result says the cart was already paid with the saved PayPal account, say so plainly.

Rules of the policy engine you should know: the minimum-rating rule only applies to products that have a rating; a product with no reviews passes it (say so in your reason and prefer rated alternatives when they exist). The per-item cap applies to the unit price; the budget applies to the cart total including quantities.

Never invent products, prices or ratings; only use what the tools return. Never propose a cart that failed check_mandate. Keep your messages concise and use plain text with short bullet lists where helpful.`

// Run executes a brand-new mission: it creates the session, sends the goal and
// records the outcome on the mission.
func (s *Shopper) Run(ctx context.Context, missionID string) {
	m, err := s.Store.GetMission(missionID)
	if err != nil {
		return
	}
	md, err := s.Store.GetMandate(m.MandateID)
	if err != nil {
		s.fail(missionID, "mandate not found")
		return
	}
	sess := s.session(missionID, true)
	msg := fmt.Sprintf("MANDATE (JSON):\n%s\n\nREMAINING BUDGET: %.2f %s\n\nGOAL: %s\n\nResearch the goal and propose a cart, or explain why it cannot be met.",
		mustJSON(mandateView(md)), md.Remaining(), md.Currency, m.Goal)
	s.converse(ctx, missionID, sess, msg)
}

// Resume continues a mission after a human rejected a proposal.
func (s *Shopper) Resume(ctx context.Context, missionID, feedback string) {
	m, err := s.Store.GetMission(missionID)
	if err != nil {
		return
	}
	md, err := s.Store.GetMandate(m.MandateID)
	if err != nil {
		s.fail(missionID, "mandate not found")
		return
	}
	sess, fresh := s.sessionOrNew(missionID)
	var msg string
	if fresh {
		// The server restarted and the conversation is gone: rebuild context.
		var last string
		for _, p := range s.Store.ListProposals(missionID) {
			last = fmt.Sprintf("Previous proposal %s (%s): %s", p.ID, p.Status, p.Summary)
		}
		msg = fmt.Sprintf("MANDATE (JSON):\n%s\n\nREMAINING BUDGET: %.2f %s\n\nGOAL: %s\n\n%s\n\nThe human rejected the previous proposal with this feedback: %q\n\nRevise your research and propose a new cart that addresses the feedback, or explain why it cannot be done.",
			mustJSON(mandateView(md)), md.Remaining(), md.Currency, m.Goal, last, feedback)
	} else {
		msg = fmt.Sprintf("The human rejected your proposal with this feedback: %q\n\nRemaining budget is now %.2f %s. Revise and propose a new cart that addresses the feedback, or explain why it cannot be done.", feedback, md.Remaining(), md.Currency)
	}
	s.converse(ctx, missionID, sess, msg)
}

func (s *Shopper) converse(ctx context.Context, missionID string, sess Session, userMsg string) {
	l := s.runLock(missionID)
	l.Lock()
	defer l.Unlock()
	s.Store.AddEvent(missionID, "status", "agent started", nil)
	_, _ = s.Store.UpdateMission(missionID, func(m *store.Mission) {
		m.Status = store.MissionRunning
		m.Error = ""
		m.Turns++
	})
	searches := 0
	tools := s.tools(missionID, &searches)
	emit := func(ev Event) {
		s.Store.AddEvent(missionID, ev.Type, ev.Message, ev.Data)
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	final, err := sess.Send(ctx, userMsg, tools, emit)
	if err != nil {
		s.fail(missionID, err.Error())
		return
	}
	m, _ := s.Store.GetMission(missionID)
	_, _ = s.Store.UpdateMission(missionID, func(m *store.Mission) {
		m.Summary = strings.TrimSpace(final)
		if m.Status == store.MissionRunning { // no proposal was created during this turn
			m.Status = store.MissionNoProposal
		}
	})
	if m != nil {
		s.Store.AddEvent(missionID, "done", "agent finished", map[string]any{"summary": strings.TrimSpace(final)})
	}
}

func (s *Shopper) fail(missionID, msg string) {
	_, _ = s.Store.UpdateMission(missionID, func(m *store.Mission) {
		m.Status = store.MissionFailed
		m.Error = msg
	})
	s.Store.AddEvent(missionID, "error", msg, nil)
}

func (s *Shopper) session(missionID string, replace bool) Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !replace {
		if sess, ok := s.sessions[missionID]; ok {
			return sess
		}
	}
	sess := s.Provider.NewSession(SystemPrompt)
	if _, exists := s.sessions[missionID]; !exists {
		s.order = append(s.order, missionID)
	}
	s.sessions[missionID] = sess
	for len(s.sessions) > maxSessions && len(s.order) > 0 {
		oldest := s.order[0]
		s.order = s.order[1:]
		if oldest != missionID {
			delete(s.sessions, oldest)
			delete(s.runLocks, oldest)
		}
	}
	return sess
}

func (s *Shopper) sessionOrNew(missionID string) (Session, bool) {
	s.mu.Lock()
	if sess, ok := s.sessions[missionID]; ok {
		s.mu.Unlock()
		return sess, false
	}
	s.mu.Unlock()
	return s.session(missionID, true), true
}

// --- tools ---

type cartItemInput struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
	Reason    string `json:"reason,omitempty"`
}

func (s *Shopper) tools(missionID string, searches *int) []Tool {
	return []Tool{
		{
			Name:        "search_products",
			Description: "Search the product catalog. Call this for each distinct item in the goal, with a specific query (product type plus the key attribute). Returns up to `limit` products with id, title, brand, merchant, category, price, currency, rating, reviews and attributes. Use max_price to filter by the per-item cap.",
			Properties: map[string]any{
				"query":     map[string]any{"type": "string", "description": "What to search for, e.g. 'USB-C hub with HDMI and ethernet'"},
				"max_price": map[string]any{"type": "number", "description": "Only return products at or below this unit price"},
				"category":  map[string]any{"type": "string", "description": "Optional category filter", "enum": catalog.Categories},
				"limit":     map[string]any{"type": "integer", "description": "Max results (default 8, max 25)"},
			},
			Required: []string{"query"},
			Run: func(ctx context.Context, input json.RawMessage) (string, error) {
				var in struct {
					Query    string  `json:"query"`
					MaxPrice float64 `json:"max_price"`
					Category string  `json:"category"`
					Limit    int     `json:"limit"`
				}
				if err := json.Unmarshal(input, &in); err != nil {
					return "", fmt.Errorf("invalid input: %w", err)
				}
				if *searches >= s.MaxSearches {
					return "", fmt.Errorf("search limit reached (%d); decide with the results you already have", s.MaxSearches)
				}
				*searches++
				if in.Limit <= 0 {
					in.Limit = 8
				}
				cur := ""
				if m, err := s.Store.GetMission(missionID); err == nil {
					if md, err := s.Store.GetMandate(m.MandateID); err == nil {
						cur = md.Currency
					}
				}
				res, err := s.Catalog.Search(ctx, catalog.Query{Text: in.Query, MaxPrice: in.MaxPrice, Category: in.Category, Limit: in.Limit, Currency: cur})
				if err != nil {
					return "", err
				}
				s.Store.AddEvent(missionID, "candidates", fmt.Sprintf("%d results for %q", len(res), in.Query), map[string]any{"query": in.Query, "products": res})
				if len(res) == 0 {
					return `{"products":[],"hint":"no results; try a broader or differently worded query"}`, nil
				}
				return mustJSON(map[string]any{"products": compact(res)}), nil
			},
		},
		{
			Name:        "get_product",
			Description: "Fetch full details for one product by id (description and all attributes). Use it to verify a requirement before proposing.",
			Properties:  map[string]any{"product_id": map[string]any{"type": "string"}},
			Required:    []string{"product_id"},
			Run: func(ctx context.Context, input json.RawMessage) (string, error) {
				var in struct {
					ProductID string `json:"product_id"`
				}
				if err := json.Unmarshal(input, &in); err != nil {
					return "", fmt.Errorf("invalid input: %w", err)
				}
				p, err := s.Catalog.Get(ctx, in.ProductID)
				if err != nil {
					return "", err
				}
				return mustJSON(p), nil
			},
		},
		{
			Name:        "check_mandate",
			Description: "Evaluate a cart against the mandate (budget, per-item cap, categories, merchants, ratings). Always call this with your intended cart before propose_cart. Returns allowed, violations, total and whether a human must approve.",
			Properties: map[string]any{
				"items": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
					"product_id": map[string]any{"type": "string"},
					"quantity":   map[string]any{"type": "integer", "minimum": 1},
				}, "required": []string{"product_id", "quantity"}}},
			},
			Required: []string{"items"},
			Run: func(ctx context.Context, input json.RawMessage) (string, error) {
				var in struct {
					Items []cartItemInput `json:"items"`
				}
				if err := json.Unmarshal(input, &in); err != nil {
					return "", fmt.Errorf("invalid input: %w", err)
				}
				md, cart, err := s.buildCart(ctx, missionID, in.Items)
				if err != nil {
					return "", err
				}
				d := md.Evaluate(cart)
				s.Store.AddEvent(missionID, "check", checkMessage(d), map[string]any{"decision": d, "cart": cart})
				return mustJSON(d), nil
			},
		},
		{
			Name:        "propose_cart",
			Description: "Propose the final cart for human approval. Only call it after check_mandate reported allowed=true. Each item needs a short factual reason. Returns the proposal id and its status: pending_approval (a human must approve), auto_approved (under the auto-approve threshold; sent straight to PayPal checkout), paid (under the threshold and paid at once with the PayPal account saved on the mandate) or rejected_by_policy (fix the violations and try again).",
			Properties: map[string]any{
				"items": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
					"product_id": map[string]any{"type": "string"},
					"quantity":   map[string]any{"type": "integer", "minimum": 1},
					"reason":     map[string]any{"type": "string", "description": "Why this item, in one sentence"},
				}, "required": []string{"product_id", "quantity", "reason"}}},
				"summary": map[string]any{"type": "string", "description": "One paragraph for the human: what this cart achieves and the main trade-offs"},
			},
			Required: []string{"items", "summary"},
			Run: func(ctx context.Context, input json.RawMessage) (string, error) {
				var in struct {
					Items   []cartItemInput `json:"items"`
					Summary string          `json:"summary"`
				}
				if err := json.Unmarshal(input, &in); err != nil {
					return "", fmt.Errorf("invalid input: %w", err)
				}
				for _, p := range s.Store.ListProposals(missionID) {
					if p.Status == store.ProposalPending || p.Status == store.ProposalAutoApproved || p.Status == store.ProposalApproved {
						return "", fmt.Errorf("proposal %s is already waiting on the human; do not propose again", p.ID)
					}
				}
				md, cart, err := s.buildCart(ctx, missionID, in.Items)
				if err != nil {
					return "", err
				}
				d := md.Evaluate(cart)
				p := &store.Proposal{ID: store.NewID("prop"), MissionID: missionID, MandateID: md.ID, Cart: cart, Decision: d, Summary: strings.TrimSpace(in.Summary), CreatedAt: time.Now()}
				switch {
				case !d.Allowed:
					p.Status = store.ProposalRejectedByPolicy
				case d.RequiresApproval:
					p.Status = store.ProposalPending
				default:
					p.Status = store.ProposalAutoApproved
				}
				s.Store.PutProposal(p)
				if p.Status == store.ProposalRejectedByPolicy {
					s.Store.AddEvent(missionID, "policy_block", "proposal blocked by the mandate: "+checkMessage(d), map[string]any{"proposal_id": p.ID, "decision": d})
					return mustJSON(map[string]any{"proposal_id": p.ID, "status": p.Status, "decision": d}), nil
				}
				_, _ = s.Store.UpdateMission(missionID, func(m *store.Mission) {
					m.ProposalID = p.ID
					if p.Status == store.ProposalPending {
						m.Status = store.MissionAwaitingApproval
					}
				})
				s.Store.AddEvent(missionID, "proposal", fmt.Sprintf("proposal %s created (%s): %d item(s), total %.2f %s", p.ID, p.Status, len(cart.Items), d.Total, d.Currency), map[string]any{"proposal_id": p.ID, "status": p.Status, "total": d.Total})
				if s.OnProposal != nil {
					// For auto-approved proposals the hook creates (or completes)
					// the payment and moves the mission to awaiting_payment or paid
					// itself, so readers never see the status before the order exists.
					if err := s.OnProposal(ctx, p); err != nil {
						s.Store.AddEvent(missionID, "error", "post-proposal hook failed: "+err.Error(), nil)
					}
				}
				if p.Status == store.ProposalAutoApproved {
					// No order could be created: let a human approve it from the UI.
					_, _ = s.Store.UpdateMission(missionID, func(m *store.Mission) {
						if m.Status == store.MissionRunning {
							m.Status = store.MissionAwaitingApproval
						}
					})
				}
				status, note := p.Status, "a human must approve before anything is paid"
				if cur, err := s.Store.GetProposal(p.ID); err == nil {
					status = cur.Status
				}
				switch status {
				case store.ProposalPaid:
					note = "under the auto-approve threshold: paid at once with the PayPal account saved on this mandate, no checkout needed"
				case store.ProposalApproved, store.ProposalAutoApproved:
					note = "under the auto-approve threshold: a PayPal order was created and the buyer completes checkout"
				}
				return mustJSON(map[string]any{"proposal_id": p.ID, "status": status, "payment": note, "decision": d}), nil
			},
		},
	}
}

func (s *Shopper) buildCart(ctx context.Context, missionID string, items []cartItemInput) (*mandate.Mandate, mandate.Cart, error) {
	m, err := s.Store.GetMission(missionID)
	if err != nil {
		return nil, mandate.Cart{}, err
	}
	md, err := s.Store.GetMandate(m.MandateID)
	if err != nil {
		return nil, mandate.Cart{}, err
	}
	if len(items) == 0 {
		return nil, mandate.Cart{}, errors.New("items must not be empty")
	}
	cart := mandate.Cart{Currency: md.Currency}
	for _, it := range items {
		p, err := s.Catalog.Get(ctx, it.ProductID)
		if err != nil {
			return nil, mandate.Cart{}, fmt.Errorf("product %q: %w", it.ProductID, err)
		}
		q := it.Quantity
		if q <= 0 {
			q = 1
		}
		cart.Items = append(cart.Items, mandate.CartItem{ProductID: p.ID, Title: p.Title, Merchant: p.Merchant, Category: p.Category, UnitPrice: p.Price, Currency: p.Currency, Quantity: q, Rating: p.Rating, Reason: strings.TrimSpace(it.Reason)})
	}
	return md, cart, nil
}

func checkMessage(d mandate.Decision) string {
	if d.Allowed {
		if d.RequiresApproval {
			return fmt.Sprintf("cart passes the mandate (total %.2f %s, %.2f left); human approval required", d.Total, d.Currency, d.RemainingAfter)
		}
		return fmt.Sprintf("cart passes the mandate (total %.2f %s) and is under the auto-approve threshold", d.Total, d.Currency)
	}
	msgs := make([]string, 0, len(d.Violations))
	for _, v := range d.Violations {
		msgs = append(msgs, v.Message)
	}
	return "violations: " + strings.Join(msgs, "; ")
}

func mandateView(m *mandate.Mandate) map[string]any {
	return map[string]any{
		"name": m.Name, "currency": m.Currency, "total_budget": m.TotalBudget, "spent": m.Spent, "reserved": m.Reserved,
		"max_per_item": m.MaxPerItem, "auto_approve_under": m.AutoApproveUnder, "allowed_categories": m.AllowedCategories,
		"blocked_merchants": m.BlockedMerchants, "min_rating": m.MinRating, "require_approval": m.RequireApproval,
		"saved_paypal_account": m.HasSavedPayment(),
	}
}

func compact(ps []catalog.Product) []map[string]any {
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, map[string]any{
			"id": p.ID, "title": p.Title, "brand": p.Brand, "merchant": p.Merchant, "category": p.Category,
			"price": p.Price, "currency": p.Currency, "rating": p.Rating, "reviews": p.Reviews, "attributes": p.Attributes,
		})
	}
	return out
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
