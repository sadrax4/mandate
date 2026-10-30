package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Fake is a scripted provider used by the tests and by the offline demo mode
// (LLM_PROVIDER=fake). It behaves like a disciplined agent: it searches once
// per item in the goal, checks the mandate, repairs budget problems by picking
// cheaper items, and proposes. It never talks to the network.
type Fake struct{}

// Name implements Provider.
func (Fake) Name() string { return "fake" }

// NewSession implements Provider.
func (Fake) NewSession(string) Session { return &fakeSession{} }

type fakeSession struct {
	goal     string
	lastPick []string
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
	final := fmt.Sprintf("I propose %s. The cart passed every mandate check and now waits for your approval (%s).", strings.Join(names, " and "), pr.Status)
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
