package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mandate/internal/catalog"
	"mandate/internal/mandate"
	"mandate/internal/store"
)

func setup(t *testing.T, md mandate.Mandate, goal string) (*Shopper, *store.Store, string) {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	md.ID = "mnd_test"
	md.CreatedAt = time.Now()
	st.PutMandate(&md)
	m := &store.Mission{ID: "msn_test", MandateID: md.ID, Goal: goal, Status: store.MissionRunning, CreatedAt: time.Now()}
	st.PutMission(m)
	return NewShopper(Fake{}, catalog.NewMock(), st), st, m.ID
}

func TestFakeAgentProposesWithinMandate(t *testing.T) {
	md := mandate.Mandate{Name: "desk", Currency: "USD", TotalBudget: 200, MaxPerItem: 120, AllowedCategories: []string{"computers"}, MinRating: 4.0}
	sh, st, id := setup(t, md, "a mechanical keyboard with brown switches and a usb-c hub with hdmi under $120")
	sh.Run(context.Background(), id)
	m, _ := st.GetMission(id)
	if m.Status != store.MissionAwaitingApproval {
		t.Fatalf("status = %s (%s / %s)", m.Status, m.Error, m.Summary)
	}
	props := st.ListProposals(id)
	if len(props) == 0 {
		t.Fatal("no proposal")
	}
	p := props[len(props)-1]
	if p.Status != store.ProposalPending || !p.Decision.Allowed {
		t.Fatalf("proposal %s allowed=%v", p.Status, p.Decision.Allowed)
	}
	if len(p.Cart.Items) != 2 {
		t.Fatalf("expected 2 items, got %d: %+v", len(p.Cart.Items), p.Cart.Items)
	}
	if p.Decision.Total > 200 {
		t.Fatalf("over budget: %.2f", p.Decision.Total)
	}
	for _, it := range p.Cart.Items {
		if it.UnitPrice > 120 || it.Category != "computers" {
			t.Fatalf("item violates mandate: %+v", it)
		}
	}
	var sawCandidates, sawCheck bool
	for _, ev := range st.ListEvents(id, 0) {
		switch ev.Type {
		case "candidates":
			sawCandidates = true
		case "check":
			sawCheck = true
		}
	}
	if !sawCandidates || !sawCheck {
		t.Fatalf("expected candidates and check events (candidates=%v check=%v)", sawCandidates, sawCheck)
	}
}

func TestFakeAgentRepairsBudget(t *testing.T) {
	// Budget only allows the cheapest keyboard; the agent must downgrade.
	md := mandate.Mandate{Name: "tight", Currency: "USD", TotalBudget: 60, AllowedCategories: []string{"computers"}}
	sh, st, id := setup(t, md, "mechanical keyboard brown switches")
	sh.Run(context.Background(), id)
	m, _ := st.GetMission(id)
	if m.Status != store.MissionAwaitingApproval {
		t.Fatalf("status = %s err=%s summary=%s", m.Status, m.Error, m.Summary)
	}
	p := st.ListProposals(id)[0]
	if p.Cart.Items[0].UnitPrice > 60 {
		t.Fatalf("did not repair: %+v", p.Cart.Items[0])
	}
}

func TestFakeAgentGivesUpWhenImpossible(t *testing.T) {
	md := mandate.Mandate{Name: "impossible", Currency: "USD", TotalBudget: 10, AllowedCategories: []string{"computers"}}
	sh, st, id := setup(t, md, "mechanical keyboard")
	sh.Run(context.Background(), id)
	m, _ := st.GetMission(id)
	if m.Status != store.MissionNoProposal {
		t.Fatalf("status = %s", m.Status)
	}
	if len(st.ListProposals(id)) != 0 {
		t.Fatal("should not have proposed")
	}
	if m.Suggestion == nil || m.Suggestion.Field != "total_budget" || m.Suggestion.Reason == "" {
		t.Fatalf("expected a budget suggestion, got %+v", m.Suggestion)
	}
	if v, ok := m.Suggestion.Value.(float64); !ok || v <= 10 {
		t.Fatalf("suggested budget should exceed the current 10: %v", m.Suggestion.Value)
	}
}

// With money already spent, the budget the agent suggests must cover what is
// spent plus the cheapest cart, not just the cart: a smaller number would
// lower the budget and fail again.
func TestFakeBudgetSuggestionCountsSpent(t *testing.T) {
	md := mandate.Mandate{Name: "nearly spent", Currency: "USD", TotalBudget: 60, Spent: 50, AllowedCategories: []string{"computers"}}
	sh, st, id := setup(t, md, "mechanical keyboard")
	sh.Run(context.Background(), id)
	m, _ := st.GetMission(id)
	if m.Suggestion == nil || m.Suggestion.Field != "total_budget" {
		t.Fatalf("expected a budget suggestion, got %+v", m.Suggestion)
	}
	if v, ok := m.Suggestion.Value.(float64); !ok || v <= 60 {
		t.Fatalf("suggested budget %v must exceed the current 60 (50 already spent)", m.Suggestion.Value)
	}
}

// A failure after the cart is already waiting keeps the cart usable.
func TestFailKeepsAWaitingCart(t *testing.T) {
	md := mandate.Mandate{Name: "x", Currency: "USD", TotalBudget: 500}
	sh, st, id := setup(t, md, "mechanical keyboard")
	_, _ = st.UpdateMission(id, func(m *store.Mission) { m.Status = store.MissionAwaitingApproval })
	sh.fail(id, "agent stopped after 12 turns")
	m, _ := st.GetMission(id)
	if m.Status != store.MissionAwaitingApproval || m.Error == "" {
		t.Fatalf("status %s, error %q", m.Status, m.Error)
	}
	_, _ = st.UpdateMission(id, func(m *store.Mission) { m.Status = store.MissionRunning })
	sh.fail(id, "boom")
	if m, _ := st.GetMission(id); m.Status != store.MissionFailed {
		t.Fatalf("a running mission should fail, got %s", m.Status)
	}
}

func TestMoneyAndPlural(t *testing.T) {
	for in, want := range map[float64]string{0: "$0.00", 39: "$39.00", 1234.5: "$1,234.50", 1000000: "$1,000,000.00", 135.36: "$135.36", 0.1 + 0.2: "$0.30"} {
		if got := Money(in, "USD"); got != want {
			t.Errorf("Money(%v) = %q, want %q", in, got, want)
		}
	}
	if Money(12, "GBP") != "£12.00" || Money(12, "CHF") != "12.00 CHF" {
		t.Errorf("currencies: %q %q", Money(12, "GBP"), Money(12, "CHF"))
	}
	if Plural(1, "item") != "1 item" || Plural(3, "round") != "3 rounds" {
		t.Error("plural")
	}
	if inSentence("A two-hour shoot") != "a two-hour shoot" || inSentence("USB-C hub") != "USB-C hub" {
		t.Errorf("inSentence: %q %q", inSentence("A two-hour shoot"), inSentence("USB-C hub"))
	}
}

// A deal sent back without a reason still tells the next buyer agent to aim lower.
func TestBuyerPromptCarriesARejectionWithoutReason(t *testing.T) {
	if p := buyerPrompt("logo", 100, "USD", "", 90); !strings.Contains(p, "without giving a reason") || !strings.Contains(p, "90.00") {
		t.Fatalf("prompt lacks the rejection: %s", p)
	}
	if p := buyerPrompt("logo", 100, "USD", "", 0); strings.Contains(p, "rejected") {
		t.Fatal("a first run must not mention a rejection")
	}
}

func TestAmendMandate(t *testing.T) {
	md := mandate.Mandate{Name: "x", Currency: "USD", TotalBudget: 100, MaxPerItem: 50, AllowedCategories: []string{"computers"}, BlockedMerchants: []string{"BudgetBytes"}, Spent: 30}
	out, desc, err := md.Amend("max_per_item", 80.0)
	if err != nil || out.MaxPerItem != 80 || desc == "" || md.MaxPerItem != 50 {
		t.Fatalf("amend cap: %v %q %+v", err, desc, out)
	}
	if _, _, err := md.Amend("total_budget", 20.0); err == nil {
		t.Fatal("budget below spent must be refused")
	}
	out, _, err = md.Amend("allowed_categories", []any{"kitchen", "computers"})
	if err != nil || len(out.AllowedCategories) != 2 {
		t.Fatalf("add category: %v %v", err, out.AllowedCategories)
	}
	out, _, err = md.Amend("blocked_merchants", "budgetbytes")
	if err != nil || len(out.BlockedMerchants) != 0 {
		t.Fatalf("unblock: %v %v", err, out.BlockedMerchants)
	}
	if _, _, err := md.Amend("min_rating", "4.5"); err != nil {
		t.Fatalf("string number: %v", err)
	}
	if _, _, err := md.Amend("name", "x"); err == nil {
		t.Fatal("unknown field must be refused")
	}
}

func TestAutoApproveAndHook(t *testing.T) {
	// The hook owns the transition to awaiting_payment (it creates the order),
	// so readers never see that status before an order exists.
	md := mandate.Mandate{Name: "petty cash", Currency: "USD", TotalBudget: 100, AutoApproveUnder: 60}
	sh, st, id := setup(t, md, "aeropress coffee maker")
	called := ""
	sh.OnProposal = func(_ context.Context, p *store.Proposal) error {
		called = p.Status
		_, _ = st.UpdateMission(p.MissionID, func(m *store.Mission) { m.Status = store.MissionAwaitingPayment })
		return nil
	}
	sh.Run(context.Background(), id)
	m, _ := st.GetMission(id)
	if m.Status != store.MissionAwaitingPayment || called != store.ProposalAutoApproved {
		t.Fatalf("status=%s hook=%q", m.Status, called)
	}

	// A hook that cannot create an order leaves the proposal for a human.
	sh2, st2, id2 := setup(t, md, "aeropress coffee maker")
	sh2.OnProposal = func(context.Context, *store.Proposal) error { return context.DeadlineExceeded }
	sh2.Run(context.Background(), id2)
	m2, _ := st2.GetMission(id2)
	if m2.Status != store.MissionAwaitingApproval {
		t.Fatalf("status=%s, want awaiting_approval when the order could not be created", m2.Status)
	}
}

func TestResumeAfterRejection(t *testing.T) {
	md := mandate.Mandate{Name: "desk", Currency: "USD", TotalBudget: 400, AllowedCategories: []string{"computers"}}
	sh, st, id := setup(t, md, "mechanical keyboard brown switches")
	sh.Run(context.Background(), id)
	first := st.ListProposals(id)[0]
	_, _ = st.UpdateProposal(first.ID, func(p *store.Proposal) { p.Status = store.ProposalRejected; p.Feedback = "too expensive" })
	_, _ = st.UpdateMission(id, func(m *store.Mission) { m.Status = store.MissionRunning; m.ProposalID = "" })
	sh.Resume(context.Background(), id, "too expensive, pick something cheaper")
	props := st.ListProposals(id)
	if len(props) != 2 {
		t.Fatalf("expected a second proposal, got %d", len(props))
	}
	if props[1].Cart.Items[0].ProductID == first.Cart.Items[0].ProductID {
		t.Fatalf("resume proposed the same product %s", first.Cart.Items[0].ProductID)
	}
	m, _ := st.GetMission(id)
	if m.Status != store.MissionAwaitingApproval || m.Turns != 2 {
		t.Fatalf("status=%s turns=%d", m.Status, m.Turns)
	}
}

func TestNegotiationConvergesInsideTheMandate(t *testing.T) {
	md := mandate.Mandate{Name: "services", Currency: "USD", TotalBudget: 500, MaxPerItem: 150}
	sh, st, id := setup(t, md, "a two-hour product photo shoot for 20 items")
	sh.Negotiate(context.Background(), id, "")
	m, _ := st.GetMission(id)
	if m.Status != store.MissionAwaitingApproval || m.Negotiation == nil || m.Negotiation.Outcome != "agreed" {
		t.Fatalf("status=%s neg=%+v err=%s", m.Status, m.Negotiation, m.Error)
	}
	if m.Negotiation.AgreedPrice > 150 || m.Negotiation.AgreedPrice <= 0 || m.Negotiation.ListPrice <= 150 {
		t.Fatalf("agreed %.2f list %.2f", m.Negotiation.AgreedPrice, m.Negotiation.ListPrice)
	}
	if len(m.Negotiation.Rounds) < 3 {
		t.Fatalf("too few rounds: %+v", m.Negotiation.Rounds)
	}
	for _, r := range m.Negotiation.Rounds {
		if r.Party == "buyer" && r.Price > 150 {
			t.Fatalf("buyer exceeded the limit: %+v", r)
		}
	}
	ps := st.ListProposals(id)
	if len(ps) != 1 || ps[0].Cart.Items[0].Category != "services" || ps[0].Cart.Items[0].UnitPrice != m.Negotiation.AgreedPrice {
		t.Fatalf("proposal = %+v", ps)
	}

	// a mandate that does not allow services: the deal is blocked and a fix is suggested
	md2 := mandate.Mandate{Name: "computers only", Currency: "USD", TotalBudget: 500, MaxPerItem: 150, AllowedCategories: []string{"computers"}}
	sh2, st2, id2 := setup(t, md2, "logo design, three concepts")
	sh2.Negotiate(context.Background(), id2, "")
	m2, _ := st2.GetMission(id2)
	if m2.Status != store.MissionNoProposal || m2.Suggestion == nil || m2.Suggestion.Field != "allowed_categories" {
		t.Fatalf("status=%s suggestion=%+v", m2.Status, m2.Suggestion)
	}
}

func TestPaidMissionRefusesAnotherCart(t *testing.T) {
	sh, st, missionID := setup(t, mandate.Mandate{Name: "Desk", Currency: "USD", TotalBudget: 300, AutoApproveUnder: 50}, "aeropress coffee maker")
	m, _ := st.GetMission(missionID)
	st.PutProposal(&store.Proposal{ID: "prop_paid", MissionID: missionID, MandateID: m.MandateID, Status: store.ProposalPaid, CreatedAt: time.Now()})
	var n int
	var propose Tool
	for _, tl := range sh.tools(missionID, &n) {
		if tl.Name == "propose_cart" {
			propose = tl
		}
	}
	if propose.Run == nil {
		t.Fatal("no propose_cart tool")
	}
	_, err := propose.Run(context.Background(), json.RawMessage(`{"items":[{"product_id":"p1","quantity":1}],"summary":"again"}`))
	if err == nil || !strings.Contains(err.Error(), "already paid") {
		t.Fatalf("a paid mission must refuse a second cart, got %v", err)
	}
	// and never more than the cap, whatever their status
	for i := 0; i < MaxProposalsPerMission; i++ {
		st.PutProposal(&store.Proposal{ID: fmt.Sprintf("prop_%d", i), MissionID: missionID, MandateID: m.MandateID, Status: store.ProposalRejected, CreatedAt: time.Now()})
	}
	_, err = propose.Run(context.Background(), json.RawMessage(`{"items":[{"product_id":"p1","quantity":1}],"summary":"again"}`))
	if err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("proposal cap: %v", err)
	}
}
