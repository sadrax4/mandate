package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mandate/internal/mandate"
)

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "mandate.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.PutMandate(&mandate.Mandate{ID: "m1", Name: "x", Currency: "USD", TotalBudget: 10, CreatedAt: time.Now()})
	st.PutMission(&Mission{ID: "ms1", MandateID: "m1", Goal: "g", Status: MissionRunning, CreatedAt: time.Now()})
	for i := 0; i < 50; i++ { // bursts of events coalesce into one write
		st.AddEvent("ms1", "status", "e", map[string]any{"i": i})
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st2.GetMandate("m1"); err != nil {
		t.Fatal("mandate not persisted")
	}
	m, err := st2.GetMission("ms1")
	if err != nil {
		t.Fatal("mission not persisted")
	}
	if m.Status != MissionFailed { // a run interrupted by a restart is marked failed
		t.Fatalf("status after reload = %s", m.Status)
	}
	if got := len(st2.ListEvents("ms1", 0)); got != 50 {
		t.Fatalf("events persisted = %d", got)
	}
}

func TestSubscribeDelivers(t *testing.T) {
	st, _ := Open("")
	ch, unsub := st.Subscribe("ms1")
	defer unsub()
	st.AddEvent("ms1", "status", "hello", nil)
	select {
	case ev := <-ch:
		if ev.Message != "hello" || ev.ID == 0 {
			t.Fatalf("bad event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
}

func TestOpenMigratesLegacyReservations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.json")
	legacy := `{"mandates":{},"missions":{},"proposals":{},"payments":{
	  "A":{"id":"A","status":"PAYER_ACTION_REQUIRED","amount":10,"currency":"USD"},
	  "B":{"id":"B","status":"COMPLETED","amount":5,"currency":"USD"},
	  "C":{"id":"C","status":"CANCELLED","amount":7,"currency":"USD"}},"events":[],"event_seq":0}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetPayment("A")
	b, _ := st.GetPayment("B")
	c, _ := st.GetPayment("C")
	if !a.Reserved || b.Reserved || c.Reserved {
		t.Fatalf("migration: A=%v B=%v C=%v", a.Reserved, b.Reserved, c.Reserved)
	}
	// entity writes are on disk immediately
	st.PutPayment(&Payment{ID: "D", Status: "CREATED", Amount: 1, Currency: "USD", Reserved: true})
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"D"`) || !strings.Contains(string(data), fmt.Sprintf(`"version": %d`, currentVersion)) {
		t.Fatalf("synchronous persistence missing: %s", data)
	}
	st.Close()
}

func TestMigrationMarksThePreexistingDemoMandate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	demoShape := `"name":"Desk refresh","currency":"USD","total_budget":300,"max_per_item":150,"auto_approve_under":40`
	old := `{"version":2,"mandates":{"mnd_b":{"id":"mnd_b",` + demoShape + `,"created_at":"2026-02-01T00:00:00Z"},"mnd_a":{"id":"mnd_a",` + demoShape + `,"created_at":"2026-01-01T00:00:00Z"},"mnd_0":{"id":"mnd_0","name":"Desk refresh","currency":"USD","total_budget":250,"created_at":"2025-01-01T00:00:00Z"},"mnd_c":{"id":"mnd_c","name":"Mine","currency":"USD","total_budget":50,"created_at":"2026-03-01T00:00:00Z"}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	demos := 0
	for _, m := range st.ListMandates() {
		if m.Demo {
			demos++
			if m.ID != "mnd_a" {
				t.Fatalf("the oldest mandate with the demo shape is the demo (a user's own Desk refresh is not), got %s", m.ID)
			}
		}
	}
	if demos != 1 {
		t.Fatalf("demo mandates: %d", demos)
	}
}

func TestOpenPersistsMigrationsAtOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"missions":{"msn_1":{"id":"msn_1","status":"running"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), fmt.Sprintf(`"version": %d`, currentVersion)) || !strings.Contains(string(data), `"status": "failed"`) {
		t.Fatalf("migration not on disk: %s", data)
	}
}

func TestProposalCopiesShareNothing(t *testing.T) {
	st, _ := Open("")
	p := &Proposal{ID: "p1", Cart: mandate.Cart{Items: []mandate.CartItem{{ProductID: "a", Title: "A"}}}, Decision: mandate.Decision{Checks: []string{"x"}}}
	st.PutProposal(p)
	p.Cart.Items[0].Title = "changed by the caller"
	got, _ := st.GetProposal("p1")
	if got.Cart.Items[0].Title != "A" {
		t.Fatal("stored proposal shares the caller's cart")
	}
	got.Decision.Checks[0] = "changed by a reader"
	again, _ := st.GetProposal("p1")
	if again.Decision.Checks[0] != "x" {
		t.Fatal("stored proposal shares a reader's decision")
	}
	md := &mandate.Mandate{ID: "m1", Name: "x", Currency: "USD", TotalBudget: 1, AllowedCategories: []string{"a"}}
	st.PutMandate(md)
	md.AllowedCategories[0] = "changed"
	if gm, _ := st.GetMandate("m1"); gm.AllowedCategories[0] != "a" {
		t.Fatal("stored mandate shares the caller's list")
	}
}
