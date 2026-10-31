package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Fake is a scripted provider used by the tests and by the offline demo mode
// (LLM_PROVIDER=fake). It behaves like a disciplined agent: it searches once
// per item in the goal, checks the mandate, repairs budget problems by picking
// cheaper items, and proposes. It never talks to the network.
type Fake struct {
	// Delay is an optional pause before every tool call, so the offline demo
	// can be watched while the mission runs (FAKE_AGENT_DELAY). Zero in tests.
	Delay time.Duration
}

// Name implements Provider.
func (Fake) Name() string { return "fake" }

// NewSession implements Provider.
func (f Fake) NewSession(system string) Session {
	switch {
	case strings.HasPrefix(system, buyerMarker):
		return &fakeNegotiator{role: "buyer", limit: promptNumber(system, "Your hard limit is"), delay: f.Delay}
	case strings.HasPrefix(system, sellerMarker):
		return &fakeNegotiator{role: "seller", floor: promptNumber(system, "Your absolute floor is"), list: promptNumber(system, "Your asking price is"), delay: f.Delay}
	}
	return &fakeSession{delay: f.Delay}
}

type fakeSession struct {
	goal     string
	lastPick []string
	delay    time.Duration
}

// pause waits for d unless the mission is cancelled first.
func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var (
	reUnder = regexp.MustCompile(`(?i)(?:under|below|less than|max(?:imum)?|up to|<)\s*\$?\s*([0-9]+(?:\.[0-9]+)?)`)
	reGoal  = regexp.MustCompile(`(?s)GOAL:\s*(.+?)(?:\n\n|$)`)
)

type fakeProduct struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Price    float64 `json:"price"`
	Rating   float64 `json:"rating"`
	Merchant string  `json:"merchant"`
	Category string  `json:"category"`
}

func (s *fakeSession) Send(ctx context.Context, userText string, tools []Tool, emit func(Event)) (string, error) {
	byName := map[string]Tool{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	if m := reGoal.FindStringSubmatch(userText); m != nil {
		s.goal = strings.TrimSpace(m[1])
	}
	feedback := strings.Contains(userText, "rejected")
	needs := splitNeeds(s.goal)
	cap := 0.0
	if m := reUnder.FindStringSubmatch(s.goal); m != nil {
		cap, _ = strconv.ParseFloat(m[1], 64)
	}
	call := func(name string, in any) (string, error) {
		t, ok := byName[name]
		if !ok {
			return "", fmt.Errorf("fake: no tool %s", name)
		}
		if err := pause(ctx, s.delay); err != nil {
			return "", err
		}
		b, _ := json.Marshal(in)
		emit(Event{Type: "tool_call", Message: name, Data: map[string]any{"tool": name, "input": json.RawMessage(b)}})
		out, err := t.Run(ctx, b)
		if err != nil {
			emit(Event{Type: "tool_result", Message: name + ": " + err.Error(), Data: map[string]any{"tool": name, "error": err.Error()}})
			return "", err
		}
		emit(Event{Type: "tool_result", Message: name + " ok", Data: map[string]any{"tool": name, "bytes": len(out)}})
		return out, nil
	}

	emit(Event{Type: "assistant", Message: fmt.Sprintf("I will search for %d item(s) and check each cart against the mandate before proposing.", len(needs))})
	var choices []choice
	for _, need := range needs {
		q := map[string]any{"query": need, "limit": 8}
		if cap > 0 {
			q["max_price"] = cap
		}
		out, err := call("search_products", q)
		if err != nil {
			continue
		}
		var res struct {
			Products []fakeProduct `json:"products"`
		}
		_ = json.Unmarshal([]byte(out), &res)
		if len(res.Products) == 0 {
			continue
		}
		c := choice{need: need, products: res.Products}
		if feedback && len(res.Products) > 1 {
			// after a rejection, move to the next cheapest candidate the human has not seen
			c.idx = cheapestIndexExcluding(res.Products, s.lastPick)
		} else {
			c.idx = bestValueIndex(res.Products)
		}
		choices = append(choices, c)
	}
	if len(choices) == 0 {
		return "I could not find any products matching the goal, so I am not proposing a cart.", nil
	}
	// Try up to 4 repair rounds: when the mandate rejects the cart, swap the
	// most expensive item for a cheaper candidate.
	for round := 0; round < 4; round++ {
		items := make([]map[string]any, 0, len(choices))
		for _, c := range choices {
			items = append(items, map[string]any{"product_id": c.products[c.idx].ID, "quantity": 1})
		}
		out, err := call("check_mandate", map[string]any{"items": items})
		if err != nil {
			return "", err
		}
		var d struct {
			Allowed    bool `json:"allowed"`
			Violations []struct {
				Code      string `json:"code"`
				ProductID string `json:"product_id"`
			} `json:"violations"`
		}
		_ = json.Unmarshal([]byte(out), &d)
		if d.Allowed {
			break
		}
		repaired := false
		for _, v := range d.Violations {
			for i := range choices {
				c := &choices[i]
				if v.ProductID != "" && c.products[c.idx].ID != v.ProductID {
					continue
				}
				if v.Code == "over_budget" && !isMostExpensive(choices, i) {
					continue
				}
				if next := cheaperAlternative(c.products, c.idx); next >= 0 {
					c.idx = next
					repaired = true
					break
				}
			}
			if repaired {
				break
			}
		}
		if !repaired {
			if field, value, reason := suggestFix(out, choices); field != "" {
				_, _ = call("suggest_mandate_change", map[string]any{"field": field, "value": value, "reason": reason})
			}
			return fmt.Sprintf("The goal cannot be met within the mandate: %s. Consider raising the budget or the per-item cap.", summarise(out)), nil
		}
	}
	items := make([]map[string]any, 0, len(choices))
	var names []string
	s.lastPick = nil
	for _, c := range choices {
		p := c.products[c.idx]
		s.lastPick = append(s.lastPick, p.ID)
		names = append(names, p.Title)
		items = append(items, map[string]any{"product_id": p.ID, "quantity": 1, "reason": fmt.Sprintf("Best value for %q: rated %.1f at %.2f from %s", c.need, p.Rating, p.Price, p.Merchant)})
	}
	summary := fmt.Sprintf("Proposed %d item(s): %s. Each item was the best-rated option within the limits of the mandate.", len(items), strings.Join(names, "; "))
	out, err := call("propose_cart", map[string]any{"items": items, "summary": summary})
	if err != nil {
		return "", err
	}
	var pr struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal([]byte(out), &pr)
	if pr.Status == "rejected_by_policy" {
		return "The mandate blocked the final cart; please adjust the budget or the rules.", nil
	}
	outcome := "now waits for your approval"
	switch pr.Status {
	case "auto_approved":
		outcome = "is under the auto-approve threshold, but the payment could not be started automatically, so it waits for your go-ahead on the mission page"
	case "approved":
		outcome = "is approved; PayPal checkout is next"
	case "paid":
		outcome = "has been paid with the PayPal account saved on this mandate"
	}
	final := fmt.Sprintf("I propose %s. The cart passed every mandate check and %s.", strings.Join(names, " and "), outcome)
	emit(Event{Type: "assistant", Message: final})
	return final, nil
}

func splitNeeds(goal string) []string {
	g := goal
	if i := strings.Index(strings.ToLower(g), " under "); i > 0 {
		g = g[:i]
	}
	parts := regexp.MustCompile(`(?i)\s*(?:,|\band\b|\bplus\b|;)\s*`).Split(g, -1)
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(strings.Trim(p, ". "))
		words := strings.Fields(p)
		if len(words) == 0 {
			continue
		}
		if len(words) == 1 {
			switch strings.ToLower(words[0]) {
			case "both", "each", "all", "please", "thanks", "asap", "cheap", "quickly":
				continue
			}
		}
		if len(p) >= 3 {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = []string{goal}
	}
	return out
}

// bestValueIndex favours search relevance (the catalog ranks by relevance),
// then rating, then price, so a "USB-C hub" query does not end up choosing a
// highly rated SSD that merely mentions USB-C.
func bestValueIndex(ps []fakeProduct) int {
	best, bestScore := 0, -1e9
	for i, p := range ps {
		score := float64(len(ps)-i)*5 + p.Rating*2 - p.Price/50
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

func cheapestIndexExcluding(ps []fakeProduct, exclude []string) int {
	ex := map[string]bool{}
	for _, e := range exclude {
		ex[e] = true
	}
	best := -1
	for i, p := range ps {
		if ex[p.ID] {
			continue
		}
		if best < 0 || p.Price < ps[best].Price {
			best = i
		}
	}
	if best < 0 {
		return 0
	}
	return best
}

func cheaperAlternative(ps []fakeProduct, cur int) int {
	best := -1
	for i, p := range ps {
		if p.Price < ps[cur].Price && (best < 0 || p.Price > ps[best].Price) {
			best = i // the most expensive option that is still cheaper than the current one
		}
	}
	return best
}

type choice struct {
	need     string
	products []fakeProduct
	idx      int
}

func isMostExpensive(cs []choice, i int) bool {
	for j, c := range cs {
		if j != i && c.products[c.idx].Price > cs[i].products[cs[i].idx].Price {
			return false
		}
	}
	return true
}

func summarise(decisionJSON string) string {
	var d struct {
		Violations []struct {
			Message string `json:"message"`
		} `json:"violations"`
	}
	_ = json.Unmarshal([]byte(decisionJSON), &d)
	var msgs []string
	for _, v := range d.Violations {
		msgs = append(msgs, v.Message)
	}
	return strings.Join(msgs, "; ")
}

// suggestFix derives the smallest mandate change from the first violation,
// the way the real agent is asked to: the cheapest candidate's price for a
// cap or budget problem, its category or merchant for a rule problem.
func suggestFix(decisionJSON string, choices []choice) (field string, value any, reason string) {
	var d struct {
		Total      float64 `json:"total"`
		Violations []struct {
			Code      string `json:"code"`
			ProductID string `json:"product_id"`
			Message   string `json:"message"`
		} `json:"violations"`
	}
	_ = json.Unmarshal([]byte(decisionJSON), &d)
	find := func(id string) *fakeProduct {
		for _, c := range choices {
			for i := range c.products {
				if c.products[i].ID == id {
					return &c.products[i]
				}
			}
		}
		if len(choices) > 0 {
			return &choices[0].products[choices[0].idx]
		}
		return nil
	}
	for _, v := range d.Violations {
		p := find(v.ProductID)
		switch v.Code {
		case "over_budget":
			cheapest := 0.0
			for _, c := range choices {
				cheapest += c.products[cheapestIndexExcluding(c.products, nil)].Price
			}
			if cheapest <= 0 {
				cheapest = d.Total
			}
			return "total_budget", cheapest, fmt.Sprintf("the cheapest cart that meets the goal costs %.2f; %s", cheapest, v.Message)
		case "item_over_cap":
			if p != nil {
				return "max_per_item", p.Price, fmt.Sprintf("the cheapest matching item costs %.2f; %s", p.Price, v.Message)
			}
		case "category_not_allowed":
			if p != nil && p.Category != "" {
				return "allowed_categories", []string{p.Category}, fmt.Sprintf("the goal needs a %s item; %s", p.Category, v.Message)
			}
		case "merchant_blocked":
			if p != nil {
				return "blocked_merchants", []string{p.Merchant}, fmt.Sprintf("the best match is only sold by %s; %s", p.Merchant, v.Message)
			}
		case "rating_too_low":
			if p != nil {
				return "min_rating", p.Rating, fmt.Sprintf("the best match is rated %.1f; %s", p.Rating, v.Message)
			}
		}
	}
	return "", nil, ""
}

// fakeNegotiator is the scripted counterpart of the negotiation prompts: a
// buyer that opens low and converges to its limit, a seller that concedes
// in decreasing steps down to its floor.
type fakeNegotiator struct {
	role                 string
	limit, floor, list   float64
	lastOffer, lastPrice float64
	round                int
	delay                time.Duration
}

var reMoney = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)`)

func promptNumber(s, label string) float64 {
	i := strings.Index(s, label)
	if i < 0 {
		return 0
	}
	if m := reMoney.FindStringSubmatch(s[i+len(label):]); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v
	}
	return 0
}

func (n *fakeNegotiator) Send(ctx context.Context, userText string, tools []Tool, emit func(Event)) (string, error) {
	n.round++
	call := func(name string, in map[string]any) {
		for _, t := range tools {
			if t.Name == name {
				if pause(ctx, n.delay) != nil {
					return
				}
				b, _ := json.Marshal(in)
				emit(Event{Type: "tool_call", Message: name, Data: map[string]any{"tool": name, "input": json.RawMessage(b)}})
				_, _ = t.Run(ctx, b)
				return
			}
		}
	}
	if n.role == "buyer" {
		price := promptNumber(userText, "Their current price:")
		if price <= n.limit && (n.round >= 4 || price-n.lastOffer < 0.08*price) {
			call("accept_offer", map[string]any{"price": price, "message": "That works, we have a deal."})
			return "", nil
		}
		offer := n.lastOffer + 0.5*(price-n.lastOffer)
		if n.lastOffer == 0 {
			offer = 0.6 * price
			if offer > 0.75*n.limit {
				offer = 0.75 * n.limit
			}
		}
		if offer > n.limit {
			offer = n.limit
		}
		offer = float64(int(offer*100)) / 100
		n.lastOffer = offer
		msg := "That is above what I can justify for this; I can offer this."
		if n.round > 1 {
			msg = "I appreciate you moving; this is closer to the value I see."
		}
		call("make_offer", map[string]any{"price": offer, "message": msg})
		return "", nil
	}
	offer := promptNumber(userText, "Their offer:")
	if n.lastPrice == 0 {
		n.lastPrice = n.list
	}
	if offer >= n.floor && (n.round >= 2 || offer >= 0.95*n.list) {
		call("accept_offer", map[string]any{"price": offer, "message": "Agreed, I can do it for that."})
		return "", nil
	}
	counter := n.lastPrice - 0.4*(n.lastPrice-offer)
	if counter < n.floor {
		counter = n.floor
	}
	counter = float64(int(counter*100)) / 100
	n.lastPrice = counter
	call("counter_offer", map[string]any{"price": counter, "message": "My rate reflects the time and gear involved, but I can come down to this."})
	return "", nil
}
