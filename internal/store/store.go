// Package store keeps every entity in memory and persists them to a JSON file
// after each change. It is deliberately simple: a hackathon project does not
// need a database, but judges should be able to restart the server without
// losing the mandates and ledger they just created.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"mandate/internal/mandate"
)

// Mission status values.
const (
	MissionRunning          = "running"
	MissionAwaitingApproval = "awaiting_approval"
	MissionAwaitingPayment  = "awaiting_payment"
	MissionPaid             = "paid"
	MissionCancelled        = "cancelled"
	MissionNoProposal       = "no_proposal"
	MissionFailed           = "failed"
	MissionRefunded         = "refunded"
)

// Proposal status values.
const (
	ProposalPending          = "pending_approval"
	ProposalAutoApproved     = "auto_approved"
	ProposalApproved         = "approved"
	ProposalRejected         = "rejected"
	ProposalRejectedByPolicy = "rejected_by_policy"
	ProposalPaid             = "paid"
	ProposalCancelled        = "cancelled"
	ProposalRefunded         = "refunded"
)

// Mission is one goal the agent works on under a mandate.
type Mission struct {
	ID         string    `json:"id"`
	MandateID  string    `json:"mandate_id"`
	Goal       string    `json:"goal"`
	Status     string    `json:"status"`
	Summary    string    `json:"summary,omitempty"`
	Error      string    `json:"error,omitempty"`
	ProposalID string    `json:"proposal_id,omitempty"`
	OrderID    string    `json:"order_id,omitempty"`
	Turns      int       `json:"turns"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Proposal is a cart the agent asked a human to approve.
type Proposal struct {
	ID        string           `json:"id"`
	MissionID string           `json:"mission_id"`
	MandateID string           `json:"mandate_id"`
	Cart      mandate.Cart     `json:"cart"`
	Decision  mandate.Decision `json:"decision"`
	Summary   string           `json:"summary"`
	Status    string           `json:"status"`
	Feedback  string           `json:"feedback,omitempty"`
	OrderID   string           `json:"order_id,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	DecidedAt *time.Time       `json:"decided_at,omitempty"`
}

// Payment is a PayPal order created for a proposal.
type Payment struct {
	ID         string  `json:"id"` // PayPal order ID
	ProposalID string  `json:"proposal_id"`
	MissionID  string  `json:"mission_id"`
	MandateID  string  `json:"mandate_id"`
	Status     string  `json:"status"`
	Amount     float64 `json:"amount"`
	Currency   string  `json:"currency"`
	ApproveURL string  `json:"approve_url,omitempty"`
	CaptureID  string  `json:"capture_id,omitempty"`
	PayerEmail string  `json:"payer_email,omitempty"`
	Mode       string  `json:"mode"`
	// Reserved is true while this payment holds a budget reservation on the
	// mandate. It is cleared exactly once, on capture, cancel or void.
	Reserved bool `json:"reserved"`
	// Vaulted is true when the order was paid with the PayPal account saved
	// on the mandate, without a checkout hop.
	Vaulted   bool      `json:"vaulted"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Event is one line in a mission's activity log.
type Event struct {
	ID        int64           `json:"id"`
	MissionID string          `json:"mission_id"`
	Type      string          `json:"type"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data,omitempty"`
	At        time.Time       `json:"at"`
}

type state struct {
	Mandates  map[string]*mandate.Mandate `json:"mandates"`
	Missions  map[string]*Mission         `json:"missions"`
	Proposals map[string]*Proposal        `json:"proposals"`
	Payments  map[string]*Payment         `json:"payments"`
	Events    []*Event                    `json:"events"`
	EventSeq  int64                       `json:"event_seq"`
}

// Store is safe for concurrent use. Writes are persisted to the JSON file
// asynchronously, at most a few times per second, so bursts of agent events
// do not rewrite the file on every change; Close flushes what is pending.
type Store struct {
	mu   sync.RWMutex
	path string
	s    state
	subs map[string][]chan Event

	saveMu    sync.Mutex
	saveTimer *time.Timer
}

// ErrNotFound is returned for unknown IDs.
var ErrNotFound = errors.New("store: not found")

// Open loads the JSON file at path (if any). An empty path keeps everything in memory.
func Open(path string) (*Store, error) {
	st := &Store{path: path, subs: map[string][]chan Event{}}
	st.s = state{Mandates: map[string]*mandate.Mandate{}, Missions: map[string]*Mission{}, Proposals: map[string]*Proposal{}, Payments: map[string]*Payment{}}
	if path == "" {
		return st, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(data, &st.s); err != nil {
		return nil, err
	}
	for _, m := range st.s.Missions {
		if m.Status == MissionRunning { // a crash mid-run: mark it so the UI does not spin forever
			m.Status = MissionFailed
			m.Error = "server restarted while the agent was running; start a new mission"
		}
	}
	return st, nil
}

// persist schedules a save. It is called with st.mu held, so it must not
// block on the file; the actual write happens on a timer goroutine.
func (st *Store) persist() {
	if st.path == "" {
		return
	}
	st.saveMu.Lock()
	defer st.saveMu.Unlock()
	if st.saveTimer == nil {
		st.saveTimer = time.AfterFunc(250*time.Millisecond, st.flush)
	}
}

func (st *Store) flush() {
	st.saveMu.Lock()
	st.saveTimer = nil
	st.saveMu.Unlock()
	st.mu.RLock()
	data, err := json.MarshalIndent(st.s, "", "  ")
	st.mu.RUnlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(st.path), 0o755)
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		_ = os.Rename(tmp, st.path)
	}
}

// Close writes any pending change to disk. Call it on shutdown.
func (st *Store) Close() {
	if st.path == "" {
		return
	}
	st.saveMu.Lock()
	t := st.saveTimer
	st.saveTimer = nil
	st.saveMu.Unlock()
	if t != nil {
		t.Stop()
	}
	st.flush()
}

// NewID returns a short random identifier with a prefix.
func NewID(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// --- mandates ---

func (st *Store) PutMandate(m *mandate.Mandate) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.s.Mandates[m.ID] = m
	st.persist()
}

func (st *Store) GetMandate(id string) (*mandate.Mandate, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	m, ok := st.s.Mandates[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := *m
	return &c, nil
}

func (st *Store) ListMandates() []*mandate.Mandate {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*mandate.Mandate, 0, len(st.s.Mandates))
	for _, m := range st.s.Mandates {
		c := *m
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// UpdateMandate applies fn under the lock so budget changes are atomic.
func (st *Store) UpdateMandate(id string, fn func(m *mandate.Mandate) error) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	m, ok := st.s.Mandates[id]
	if !ok {
		return ErrNotFound
	}
	if err := fn(m); err != nil {
		return err
	}
	st.persist()
	return nil
}

// --- missions ---

func (st *Store) PutMission(m *Mission) {
	st.mu.Lock()
	defer st.mu.Unlock()
	m.UpdatedAt = time.Now()
	st.s.Missions[m.ID] = m
	st.persist()
}

func (st *Store) GetMission(id string) (*Mission, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	m, ok := st.s.Missions[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := *m
	return &c, nil
}

func (st *Store) UpdateMission(id string, fn func(m *Mission)) (*Mission, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	m, ok := st.s.Missions[id]
	if !ok {
		return nil, ErrNotFound
	}
	fn(m)
	m.UpdatedAt = time.Now()
	st.persist()
	c := *m
	return &c, nil
}

func (st *Store) ListMissions(mandateID string) []*Mission {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*Mission, 0, len(st.s.Missions))
	for _, m := range st.s.Missions {
		if mandateID != "" && m.MandateID != mandateID {
			continue
		}
		c := *m
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// --- proposals ---

func (st *Store) PutProposal(p *Proposal) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.s.Proposals[p.ID] = p
	st.persist()
}

func (st *Store) GetProposal(id string) (*Proposal, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	p, ok := st.s.Proposals[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := *p
	return &c, nil
}

func (st *Store) UpdateProposal(id string, fn func(p *Proposal)) (*Proposal, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	p, ok := st.s.Proposals[id]
	if !ok {
		return nil, ErrNotFound
	}
	fn(p)
	st.persist()
	c := *p
	return &c, nil
}

func (st *Store) ListProposals(missionID string) []*Proposal {
	st.mu.RLock()
	defer st.mu.RUnlock()
	var out []*Proposal
	for _, p := range st.s.Proposals {
		if missionID != "" && p.MissionID != missionID {
			continue
		}
		c := *p
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// --- payments ---

func (st *Store) PutPayment(p *Payment) {
	st.mu.Lock()
	defer st.mu.Unlock()
	p.UpdatedAt = time.Now()
	st.s.Payments[p.ID] = p
	st.persist()
}

func (st *Store) GetPayment(id string) (*Payment, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	p, ok := st.s.Payments[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := *p
	return &c, nil
}

func (st *Store) UpdatePayment(id string, fn func(p *Payment)) (*Payment, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	p, ok := st.s.Payments[id]
	if !ok {
		return nil, ErrNotFound
	}
	fn(p)
	p.UpdatedAt = time.Now()
	st.persist()
	c := *p
	return &c, nil
}

func (st *Store) ListPayments() []*Payment {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*Payment, 0, len(st.s.Payments))
	for _, p := range st.s.Payments {
		c := *p
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// --- events ---

// AddEvent appends to the mission log and fans out to SSE subscribers.
func (st *Store) AddEvent(missionID, typ, message string, data any) Event {
	st.mu.Lock()
	st.s.EventSeq++
	ev := Event{ID: st.s.EventSeq, MissionID: missionID, Type: typ, Message: message, At: time.Now()}
	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			ev.Data = b
		}
	}
	st.s.Events = append(st.s.Events, &ev)
	if len(st.s.Events) > 5000 { // keep the file bounded
		st.s.Events = st.s.Events[len(st.s.Events)-5000:]
	}
	subs := append([]chan Event(nil), st.subs[missionID]...)
	st.persist()
	st.mu.Unlock()
	// Deliver without blocking the store. A subscriber whose buffer is full
	// misses the event; the UI also polls, so it recovers on the next refresh.
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
	return ev
}

func (st *Store) ListEvents(missionID string, afterID int64) []Event {
	st.mu.RLock()
	defer st.mu.RUnlock()
	var out []Event
	for _, e := range st.s.Events {
		if e.MissionID == missionID && e.ID > afterID {
			out = append(out, *e)
		}
	}
	return out
}

// Subscribe returns a channel of new events for a mission and an unsubscribe func.
func (st *Store) Subscribe(missionID string) (<-chan Event, func()) {
	ch := make(chan Event, 256)
	st.mu.Lock()
	st.subs[missionID] = append(st.subs[missionID], ch)
	st.mu.Unlock()
	return ch, func() {
		st.mu.Lock()
		defer st.mu.Unlock()
		subs := st.subs[missionID]
		for i, c := range subs {
			if c == ch {
				st.subs[missionID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}
}
