package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"mandate/internal/mandate"
	"mandate/internal/store"
)

// Agent-to-agent commerce: a buyer agent, bound by the human's mandate,
// negotiates the price of a service with a seller agent. Neither agent can
// pay. The agreed price becomes an ordinary proposal: the human approves it
// (or the auto-approve threshold does) and PayPal settles it like any other
// purchase. The seller agent here is a reference counterparty so the buyer
// side can be exercised end to end; in production it is any A2A/AP2-capable
// seller.

const (
	negotiationMaxRounds = 6
	buyerMarker          = "NEGOTIATION BUYER"
	sellerMarker         = "NEGOTIATION SELLER"
)

// Negotiate runs a negotiation mission. feedback is the human's note after a
// rejected deal (empty on the first run).
func (s *Shopper) Negotiate(ctx context.Context, missionID, feedback string) {
	l := s.runLock(missionID)
	l.Lock()
	defer l.Unlock()
	m, err := s.Store.GetMission(missionID)
	if err != nil {
		return
	}
	md, err := s.Store.GetMandate(m.MandateID)
	if err != nil {
		s.fail(missionID, "mandate not found")
		return
	}
	limit := md.Remaining()
	if md.MaxPerItem > 0 && md.MaxPerItem < limit {
		limit = md.MaxPerItem
	}
	limit = mandate.Round(limit)
	if limit <= 0 {
		s.fail(missionID, "the mandate has no budget left for a service")
		return
	}
	service := strings.TrimSpace(m.Goal)
	// A deal becomes a cart in the services category: if the mandate cannot
	// buy services at all, say so now instead of after twelve model turns.
	probe := mandate.Cart{Currency: md.Currency, Items: []mandate.CartItem{{ProductID: "svc_" + missionID, Title: service, Merchant: "Seller agent", Category: "services", UnitPrice: 0.01, Currency: md.Currency, Quantity: 1}}}
	for _, v := range md.Evaluate(probe).Violations {
		if v.Code != "category_not_allowed" {
			continue
		}
		sug := &store.Suggestion{Field: "allowed_categories", Value: []string{"services"}, Reason: "a negotiated service is in the services category, which this mandate does not allow"}
		summary := "This mandate does not allow the services category, so no negotiated deal could be approved under it. Allow services (the suggested change) and retry."
		_, _ = s.Store.UpdateMission(missionID, func(x *store.Mission) {
			x.Kind = "negotiate"
			x.Suggestion = sug
			x.Summary = summary
			x.Status = store.MissionNoProposal
		})
		s.Store.AddEvent(missionID, "policy_block", "negotiation not started: "+v.Message, map[string]any{"decision_violation": v})
		s.Store.AddEvent(missionID, "suggestion", "agent suggests a mandate change: allow services ("+sug.Reason+")", map[string]any{"field": sug.Field, "value": sug.Value})
		s.Store.AddEvent(missionID, "done", summary, nil)
		return
	}
	list := mandate.Round(limit * 1.45)
	floor := mandate.Round(limit * 0.82)
	neg := &store.Negotiation{Service: service, Seller: "Seller agent", ListPrice: list, Limit: limit, Rounds: []store.NegotiationRound{}}
	_, _ = s.Store.UpdateMission(missionID, func(x *store.Mission) {
		x.Status = store.MissionRunning
		x.Error = ""
		x.Turns++
		x.Kind = "negotiate"
		x.Negotiation = neg
	})
	s.Store.AddEvent(missionID, "status", fmt.Sprintf("negotiation started: buyer agent (hard limit %.2f %s from the mandate) vs seller agent asking %.2f", limit, md.Currency, list), map[string]any{"limit": limit, "list_price": list})

	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	buyer := s.Provider.NewSession(buyerPrompt(service, limit, md.Currency, feedback))
	seller := s.Provider.NewSession(sellerPrompt(service, list, floor, md.Currency))

	type move struct {
		kind    string // offer | counter | accept | decline | walk_away
		price   float64
		message string
	}
	var last move
	record := func(party string, mv move, round int) {
		r := store.NegotiationRound{Round: round, Party: party, Move: mv.kind, Price: mv.price, Message: strings.TrimSpace(mv.message), At: time.Now()}
		neg.Rounds = append(neg.Rounds, r)
		_, _ = s.Store.UpdateMission(missionID, func(x *store.Mission) { x.Negotiation = neg })
		label := map[string]string{"offer": "offers", "counter": "counters at", "accept": "accepts", "decline": "declines", "walk_away": "walks away"}[mv.kind]
		msg := fmt.Sprintf("%s agent %s", party, label)
		if mv.price > 0 && (mv.kind == "offer" || mv.kind == "counter" || mv.kind == "accept") {
			msg += fmt.Sprintf(" %.2f %s", mv.price, md.Currency)
		}
		if r.Message != "" {
			msg += ": " + r.Message
		}
		s.Store.AddEvent(missionID, "negotiation", msg, map[string]any{"party": party, "move": mv.kind, "price": mv.price, "round": round})
	}
	toolsFor := func(party string, mv *move) []Tool {
		set := func(kind string) func(ctx context.Context, in json.RawMessage) (string, error) {
			return func(_ context.Context, in json.RawMessage) (string, error) {
				var x struct {
					Price   float64 `json:"price"`
					Message string  `json:"message"`
					Reason  string  `json:"reason"`
				}
				if err := json.Unmarshal(in, &x); err != nil {
					return "", fmt.Errorf("invalid input: %w", err)
				}
				if x.Message == "" {
					x.Message = x.Reason
				}
				*mv = move{kind: kind, price: mandate.Round(x.Price), message: x.Message}
				return `{"ok":true,"note":"recorded; stop and wait for the other side"}`, nil
			}
		}
		price := map[string]any{"type": "number"}
		msg := map[string]any{"type": "string", "description": "one or two sentences to the other side"}
		if party == "buyer" {
			return []Tool{
				{Name: "make_offer", Description: "Offer a price for the service. Never above your hard limit.", Properties: map[string]any{"price": price, "message": msg}, Required: []string{"price", "message"}, Run: set("offer")},
				{Name: "accept_offer", Description: "Accept the seller's current price (only if it is at or below your hard limit).", Properties: map[string]any{"price": price, "message": msg}, Required: []string{"price", "message"}, Run: set("accept")},
				{Name: "walk_away", Description: "End the negotiation without a deal.", Properties: map[string]any{"reason": map[string]any{"type": "string"}}, Required: []string{"reason"}, Run: set("walk_away")},
			}
		}
		return []Tool{
			{Name: "counter_offer", Description: "Counter with a new price.", Properties: map[string]any{"price": price, "message": msg}, Required: []string{"price", "message"}, Run: set("counter")},
			{Name: "accept_offer", Description: "Accept the buyer's current offer.", Properties: map[string]any{"price": price, "message": msg}, Required: []string{"price", "message"}, Run: set("accept")},
			{Name: "decline", Description: "End the negotiation without a deal.", Properties: map[string]any{"reason": map[string]any{"type": "string"}}, Required: []string{"reason"}, Run: set("decline")},
		}
	}
	emit := func(ev Event) {
		if ev.Type == "assistant" { // the agents' prose is already captured through their moves
			return
		}
		s.Store.AddEvent(missionID, ev.Type, ev.Message, ev.Data)
	}

	// The seller opens at list price.
	sellerPrice := list
	record("seller", move{kind: "counter", price: list, message: fmt.Sprintf("I can do %s for %.2f %s.", service, list, md.Currency)}, 0)
	buyerPrice := 0.0
	outcome, agreed := "no_deal", 0.0
	for round := 1; round <= negotiationMaxRounds && outcome == "no_deal"; round++ {
		// buyer's turn
		var mv move
		prompt := fmt.Sprintf("Round %d of %d. The seller says: %q Their current price: %.2f %s. Your hard limit is %.2f %s. Respond with exactly one tool call.", round, negotiationMaxRounds, lastMessage(neg, "seller"), sellerPrice, md.Currency, limit, md.Currency)
		if _, err := buyer.Send(ctx, prompt, toolsFor("buyer", &mv), emit); err != nil {
			s.fail(missionID, "buyer agent: "+err.Error())
			return
		}
		switch mv.kind {
		case "accept":
			if sellerPrice > limit { // the model may not accept above the mandate; treat as a limit offer
				mv = move{kind: "offer", price: limit, message: "I can go to my limit, " + fmt.Sprintf("%.2f", limit) + ", not above."}
			} else {
				mv.price = sellerPrice
				record("buyer", mv, round)
				outcome, agreed = "agreed", sellerPrice
				continue
			}
		case "walk_away":
			record("buyer", mv, round)
			outcome = "walked_away"
			continue
		case "":
			mv = move{kind: "offer", price: math.Min(limit, mandate.Round(buyerPrice+0.5*(sellerPrice-buyerPrice))), message: "Let us meet in the middle."}
		}
		if mv.price > limit {
			mv.price, mv.message = limit, mv.message+" (capped at the mandate limit)"
		}
		buyerPrice = mv.price
		record("buyer", mv, round)
		last = mv

		// seller's turn
		var sv move
		sprompt := fmt.Sprintf("Round %d of %d. The buyer says: %q Their offer: %.2f %s. Your last price was %.2f %s. Respond with exactly one tool call.", round, negotiationMaxRounds, last.message, buyerPrice, md.Currency, sellerPrice, md.Currency)
		if _, err := seller.Send(ctx, sprompt, toolsFor("seller", &sv), emit); err != nil {
			s.fail(missionID, "seller agent: "+err.Error())
			return
		}
		switch sv.kind {
		case "accept":
			sv.price = buyerPrice
			record("seller", sv, round)
			outcome, agreed = "agreed", buyerPrice
		case "decline":
			record("seller", sv, round)
			outcome = "declined"
		default:
			if sv.kind == "" {
				sv = move{kind: "counter", price: mandate.Round(math.Max(floor, sellerPrice-0.4*(sellerPrice-buyerPrice))), message: "I can come down a little."}
			}
			if sv.price < floor {
				sv.price = floor
			}
			if sv.price <= buyerPrice { // a counter at or below the offer is an acceptance
				sv.kind, sv.price = "accept", buyerPrice
				record("seller", sv, round)
				outcome, agreed = "agreed", buyerPrice
				continue
			}
			sellerPrice = sv.price
			record("seller", sv, round)
		}
	}

	neg.Outcome = outcome
	if outcome != "agreed" {
		summary := fmt.Sprintf("No deal: after %d round(s) the seller's lowest price was %.2f %s against a mandate limit of %.2f %s.", len(neg.Rounds), sellerPrice, md.Currency, limit, md.Currency)
		if outcome == "walked_away" || outcome == "declined" {
			summary = fmt.Sprintf("No deal: the %s ended the negotiation (%s).", map[string]string{"walked_away": "buyer agent", "declined": "seller agent"}[outcome], lastMessage(neg, ""))
		}
		_, _ = s.Store.UpdateMission(missionID, func(x *store.Mission) {
			x.Negotiation = neg
			x.Summary = summary
			x.Status = store.MissionNoProposal
		})
		if sellerPrice > limit {
			sug := &store.Suggestion{Field: "max_per_item", Value: sellerPrice, Reason: fmt.Sprintf("the seller would not go below %.2f; the mandate allows %.2f per item", sellerPrice, limit)}
			if md.MaxPerItem == 0 || md.MaxPerItem >= sellerPrice {
				sug = &store.Suggestion{Field: "total_budget", Value: mandate.Round(md.Spent + md.Reserved + sellerPrice), Reason: fmt.Sprintf("the seller would not go below %.2f; only %.2f of the budget is left", sellerPrice, md.Remaining())}
			}
			_, _ = s.Store.UpdateMission(missionID, func(x *store.Mission) { x.Suggestion = sug })
			s.Store.AddEvent(missionID, "suggestion", "agent suggests a mandate change: "+sug.Reason, map[string]any{"field": sug.Field, "value": sug.Value})
		}
		s.Store.AddEvent(missionID, "done", "negotiation ended without a deal", map[string]any{"summary": summary})
		return
	}
	neg.AgreedPrice = agreed
	_, _ = s.Store.UpdateMission(missionID, func(x *store.Mission) { x.Negotiation = neg })
	saved := list - agreed
	cart := mandate.Cart{Currency: md.Currency, Items: []mandate.CartItem{{
		ProductID: "svc_" + missionID, Title: service + " (negotiated)", Merchant: neg.Seller, Category: "services",
		UnitPrice: agreed, Currency: md.Currency, Quantity: 1,
		Reason: fmt.Sprintf("Negotiated from %.2f down to %.2f over %d round(s), inside the mandate limit of %.2f", list, agreed, len(neg.Rounds), limit),
	}}}
	summary := fmt.Sprintf("Deal reached at %.2f %s for %s (the seller asked %.2f; %.2f saved, %.0f%% below asking). The human still decides; nothing is paid until approval.", agreed, md.Currency, service, list, saved, 100*saved/list)
	out, p := s.createProposal(ctx, missionID, md, cart, summary)
	if p.Status == store.ProposalRejectedByPolicy {
		// Typically the mandate does not allow the "services" category.
		for _, v := range p.Decision.Violations {
			if v.Code == "category_not_allowed" {
				sug := &store.Suggestion{Field: "allowed_categories", Value: []string{"services"}, Reason: "a negotiated service is in the services category, which this mandate does not allow"}
				_, _ = s.Store.UpdateMission(missionID, func(x *store.Mission) { x.Suggestion = sug })
				s.Store.AddEvent(missionID, "suggestion", "agent suggests a mandate change: allow services ("+sug.Reason+")", map[string]any{"field": sug.Field, "value": sug.Value})
				break
			}
		}
		_, _ = s.Store.UpdateMission(missionID, func(x *store.Mission) {
			x.Summary = "Deal reached at " + fmt.Sprintf("%.2f %s", agreed, md.Currency) + " but the mandate blocked it: " + checkMessage(p.Decision)
			if x.Status == store.MissionRunning {
				x.Status = store.MissionNoProposal
			}
		})
		s.Store.AddEvent(missionID, "done", "negotiation finished; the mandate blocked the deal", nil)
		return
	}
	_, _ = s.Store.UpdateMission(missionID, func(x *store.Mission) { x.Summary = summary })
	s.Store.AddEvent(missionID, "done", "negotiation finished: "+summary, map[string]any{"result": out})
}

func lastMessage(n *store.Negotiation, party string) string {
	for i := len(n.Rounds) - 1; i >= 0; i-- {
		if party == "" || n.Rounds[i].Party == party {
			return n.Rounds[i].Message
		}
	}
	return ""
}

func buyerPrompt(service string, limit float64, cur, feedback string) string {
	fb := ""
	if feedback != "" {
		fb = fmt.Sprintf("\n\nThe human rejected a previous deal with this feedback: %q. Take it into account (usually: aim lower, or walk away sooner).", feedback)
	}
	return fmt.Sprintf(`%s
You are Mandate's buyer agent, negotiating on behalf of a human who wants: %s.
Your hard limit is %.2f %s. It comes from the human's mandate and you can never offer or accept more than it; you do not reveal it.
How to negotiate: open low (around 55-70%% of the asking price), concede in decreasing steps, and accept when the seller's price is at or below your limit and further haggling is unlikely to save more than a few percent. Walk away only if the seller stays above your limit after several rounds. Be courteous and brief: one or two sentences per move.
Each turn, respond with exactly one tool call (make_offer, accept_offer or walk_away) and nothing else.%s`, buyerMarker, service, limit, cur, fb)
}

func sellerPrompt(service string, list, floor float64, cur string) string {
	return fmt.Sprintf(`%s
You are an independent seller agent offering: %s.
Your asking price is %.2f %s. Your absolute floor is %.2f %s; never go below it and never reveal it.
How to negotiate: defend your value in one sentence, concede in decreasing steps, accept when the buyer's offer is at or above your floor and talks have gone two or more rounds, or when you would otherwise lose the deal. Be courteous and brief.
Each turn, respond with exactly one tool call (counter_offer, accept_offer or decline) and nothing else.`, sellerMarker, service, list, cur, floor, cur)
}
