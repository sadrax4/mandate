package agent

import (
	"context"
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
