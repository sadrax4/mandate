package store

import (
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
	if !strings.Contains(string(data), `"D"`) || !strings.Contains(string(data), `"version": 1`) {
		t.Fatalf("synchronous persistence missing: %s", data)
	}
	st.Close()
}
