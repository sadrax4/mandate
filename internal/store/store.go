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
	"log"
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

// Payment status values Mandate assigns itself (PayPal's own order and
// capture statuses are stored as PayPal reports them).
const (
	PaymentCancelled         = "CANCELLED"
	PaymentVoided            = "VOIDED"
	PaymentExpired           = "EXPIRED"
	PaymentCompleted         = "COMPLETED"
	PaymentRefunded          = "REFUNDED"
	PaymentPartiallyRefunded = "PARTIALLY_REFUNDED"
	PaymentReversed          = "REVERSED"
	PaymentDenied            = "DENIED"
	PaymentCaptureFailed     = "CAPTURE_FAILED"
	// PaymentPending is a capture PayPal has accepted but not completed yet
	// (an eCheck or a risk review); it books when PayPal completes it.
	PaymentPending = "PENDING"
)

// IsFinalPayment reports whether a payment status can never become payable
// again: it was captured (possibly refunded afterwards) or it was given up.
// Every money path must consult this one predicate.
func IsFinalPayment(status string) bool {
	switch status {
	case PaymentCompleted, PaymentPartiallyRefunded, PaymentCancelled, PaymentVoided, PaymentExpired, PaymentRefunded, PaymentReversed, PaymentDenied:
		return true
	}
	return false
}

// IsCaptured reports whether PayPal has taken, or is taking, the money for
// the order: it can no longer be captured again or withdrawn.
func IsCaptured(status string) bool {
	switch status {
	case PaymentCompleted, PaymentPartiallyRefunded, PaymentPending:
		return true
	}
	return false
}

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
	ID         string `json:"id"`
	MandateID  string `json:"mandate_id"`
	Goal       string `json:"goal"`
	Status     string `json:"status"`
	Summary    string `json:"summary,omitempty"`
	Error      string `json:"error,omitempty"`
	ProposalID string `json:"proposal_id,omitempty"`
	OrderID    string `json:"order_id,omitempty"`
	Turns      int    `json:"turns"`
	// Suggestion is the smallest mandate change the agent believes would make
	// the goal possible; it is only ever applied by a human.
	Suggestion *Suggestion `json:"suggestion,omitempty"`
	// RetryOf links a mission started by applying a suggestion to the
	// mission that produced it.
	RetryOf string `json:"retry_of,omitempty"`
	// Kind is "buy" (default: research a catalog and propose a cart) or
	// "negotiate" (a buyer agent haggles with a seller agent for a service
	// and the agreed price becomes the proposal).
	Kind        string       `json:"kind,omitempty"`
	Negotiation *Negotiation `json:"negotiation,omitempty"`
	// Standing orders: RepeatDays > 0 makes this mission re-run its goal
	// every RepeatDays days (NextRunAt is the next scheduled run, LastRunID
	// the most recent run). Each run is a normal mission with RepeatOf set.
	RepeatDays int        `json:"repeat_days,omitempty"`
	NextRunAt  *time.Time `json:"next_run_at,omitempty"`
	LastRunID  string     `json:"last_run_id,omitempty"`
	RepeatOf   string     `json:"repeat_of,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// clone returns a deep copy, so a mission handed out by the store (or kept
// by a caller) never shares pointer fields with the stored one.
func (m *Mission) clone() Mission {
	c := *m
	if m.Suggestion != nil {
		sg := *m.Suggestion
		c.Suggestion = &sg
	}
	if m.Negotiation != nil {
		n := *m.Negotiation
		n.Rounds = append([]NegotiationRound{}, m.Negotiation.Rounds...)
		c.Negotiation = &n
	}
	if m.NextRunAt != nil {
		t := *m.NextRunAt
		c.NextRunAt = &t
	}
	return c
}

// Negotiation is the transcript of a buyer agent and a seller agent
// settling on a price for a service, inside the buyer's mandate.
type Negotiation struct {
	Service     string             `json:"service"`
	Seller      string             `json:"seller"`
	ListPrice   float64            `json:"list_price"`
	Limit       float64            `json:"limit"` // the buyer agent's hard ceiling, from the mandate
	Rounds      []NegotiationRound `json:"rounds"`
	Outcome     string             `json:"outcome,omitempty"` // agreed | no_deal
	AgreedPrice float64            `json:"agreed_price,omitempty"`
}

// NegotiationRound is one move by either party.
type NegotiationRound struct {
	Round   int       `json:"round"`
	Party   string    `json:"party"` // buyer | seller
	Move    string    `json:"move"`  // offer | counter | accept | decline | walk_away
	Price   float64   `json:"price,omitempty"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Suggestion is a proposed change to one mandate field. For the numeric
// fields Value is the new number; for allowed_categories it lists categories
// to add; for blocked_merchants it lists merchants to unblock.
type Suggestion struct {
	Field            string `json:"field"`
	Value            any    `json:"value"`
	Reason           string `json:"reason"`
	AppliedMissionID string `json:"applied_mission_id,omitempty"`
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
	Vaulted bool `json:"vaulted"`
	// Refunded is the total given back so far (partial refunds accumulate).
	Refunded float64 `json:"refunded,omitempty"`
	// Accounted is set exactly once, when the capture was booked into the
	// mandate's spent total. It is the idempotency key for paid-marking.
	Accounted bool `json:"accounted"`
	// RefundIDs lists the PayPal refund (or reversal) resources already
	// credited, so a redelivered webhook never credits twice.
	RefundIDs []string `json:"refund_ids,omitempty"`
	// PendingRefunds are refunds PayPal reported before the capture itself
	// was booked (deliveries are unordered); they are applied at booking.
	PendingRefunds []PendingRefund `json:"pending_refunds,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// PendingRefund is a refund waiting for its capture to be booked.
type PendingRefund struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Amount float64 `json:"amount"`
}

// clone returns a deep copy of the payment (its slices are never shared).
func (p *Payment) clone() Payment {
	c := *p
	c.RefundIDs = append([]string(nil), p.RefundIDs...)
	c.PendingRefunds = append([]PendingRefund(nil), p.PendingRefunds...)
	return c
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
	Version   int                         `json:"version"`
	Mandates  map[string]*mandate.Mandate `json:"mandates"`
	Missions  map[string]*Mission         `json:"missions"`
	Proposals map[string]*Proposal        `json:"proposals"`
	Payments  map[string]*Payment         `json:"payments"`
	Events    []*Event                    `json:"events"`
	EventSeq  int64                       `json:"event_seq"`
}

// currentVersion is bumped when a one-time migration is added to Open.
const currentVersion = 2

// Store is safe for concurrent use. Entity writes (mandates, missions,
// proposals, payments) are written to the JSON file synchronously, so a
// money transition acknowledged to PayPal or to the browser is on disk by
// then. Events, which arrive in bursts while the agent works, are saved at
// most a few times per second; Close flushes what is pending.
type Store struct {
	mu   sync.RWMutex
	path string
	s    state
	subs map[string][]chan Event

	saveMu    sync.Mutex
	saveTimer *time.Timer
	fileMu    sync.Mutex // serialises writes of the data file
	gen       uint64     // bumped on every mutation (under mu); snapshots carry it
	written   uint64     // generation of the snapshot on disk (under fileMu)
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
	if st.s.Version < 1 {
		// Payments written before the Reserved flag existed hold a budget
		// reservation whenever they are still payable. Files from builds that
		// already had the flag (some rows carry it) are left alone.
		hasFlag := false
		for _, p := range st.s.Payments {
			if p.Reserved {
				hasFlag = true
			}
		}
		if !hasFlag {
			for _, p := range st.s.Payments {
				if !IsFinalPayment(p.Status) && p.Status != PaymentCaptureFailed {
					p.Reserved = true
				}
			}
		}
	}
	if st.s.Version < 2 {
		// Payments written before the Accounted flag: a capture was booked
		// when the proposal became paid (or was refunded afterwards).
		for _, p := range st.s.Payments {
			settled := false
			if pr, ok := st.s.Proposals[p.ProposalID]; ok {
				settled = pr.Status == ProposalPaid || pr.Status == ProposalRefunded
			}
			switch p.Status {
			case PaymentPartiallyRefunded, PaymentRefunded, PaymentReversed:
				p.Accounted = true
			case PaymentCompleted, PaymentDenied:
				p.Accounted = settled
			}
		}
	}
	st.s.Version = currentVersion
	return st, nil
}

// persistNow writes the state synchronously. The caller holds st.mu for
// writing, so the snapshot is consistent and carries the latest generation.
func (st *Store) persistNow() {
	st.gen++
	if st.path == "" {
		return
	}
	data, err := json.MarshalIndent(st.s, "", "  ")
	if err != nil {
		log.Printf("store: marshal: %v", err)
		return
	}
	st.writeFile(data, st.gen)
}

// writeFile replaces the data file atomically. Snapshots are ordered by
// generation, so a slower writer holding an older snapshot never overwrites
// a newer one.
func (st *Store) writeFile(data []byte, gen uint64) {
	st.fileMu.Lock()
	defer st.fileMu.Unlock()
	if gen <= st.written {
		return
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		log.Printf("store: mkdir: %v", err)
		return
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("store: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, st.path); err != nil {
		log.Printf("store: rename %s: %v", st.path, err)
		return
	}
	st.written = gen
}

// persist schedules a save. It is called with st.mu held, so it must not
// block on the file; the actual write happens on a timer goroutine.
func (st *Store) persist() {
	st.gen++
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
	gen := st.gen
	data, err := json.MarshalIndent(st.s, "", "  ")
	st.mu.RUnlock()
	if err != nil {
		log.Printf("store: marshal: %v", err)
		return
	}
	st.writeFile(data, gen)
}

// Close writes any pending change to disk. Call it on shutdown. A flush that
// already fired finishes first (writes are serialised), and the final write
// below is atomic, so the file is never left torn.
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
	c := *m
	st.s.Mandates[m.ID] = &c
	st.persistNow()
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
	st.persistNow()
	return nil
}

// --- missions ---

func (st *Store) PutMission(m *Mission) {
	st.mu.Lock()
	defer st.mu.Unlock()
	m.UpdatedAt = time.Now()
	c := m.clone()
	st.s.Missions[m.ID] = &c
	st.persistNow()
}

func (st *Store) GetMission(id string) (*Mission, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	m, ok := st.s.Missions[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := m.clone()
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
	detached := m.clone() // never keep pointers the caller still mutates
	*m = detached
	st.persistNow()
	c := m.clone()
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
		c := m.clone()
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// --- proposals ---

func (st *Store) PutProposal(p *Proposal) {
	st.mu.Lock()
	defer st.mu.Unlock()
	c := *p
	st.s.Proposals[p.ID] = &c
	st.persistNow()
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
	st.persistNow()
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
	c := p.clone()
	st.s.Payments[p.ID] = &c
	st.persistNow()
}

func (st *Store) GetPayment(id string) (*Payment, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	p, ok := st.s.Payments[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := p.clone()
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
	detached := p.clone() // never keep slices the caller still mutates
	*p = detached
	st.persistNow()
	c := p.clone()
	return &c, nil
}

// reservedTotals is the one place that defines what counts as reserved:
// every payment row still holding a reservation, summed per mandate.
// Call with the lock held.
func (st *Store) reservedTotals() map[string]float64 {
	totals := map[string]float64{}
	for _, p := range st.s.Payments {
		if p.Reserved {
			totals[p.MandateID] += p.Amount
		}
	}
	return totals
}

// SumReserved is the total still reserved by one mandate's payment rows
// (the same rule as reservedTotals, without building every total).
func (st *Store) SumReserved(mandateID string) float64 {
	st.mu.RLock()
	defer st.mu.RUnlock()
	total := 0.0
	for _, p := range st.s.Payments {
		if p.MandateID == mandateID && p.Reserved {
			total += p.Amount
		}
	}
	return total
}

// ReservedTotals returns every mandate's reserved total in one pass.
func (st *Store) ReservedTotals() map[string]float64 {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.reservedTotals()
}

// LivePaymentFor returns the payment row of a proposal that can still be
// paid (created or approved, not final), if any.
func (st *Store) LivePaymentFor(proposalID string) (*Payment, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, p := range st.s.Payments {
		if p.ProposalID == proposalID && !IsFinalPayment(p.Status) {
			c := p.clone()
			return &c, true
		}
	}
	return nil, false
}

func (st *Store) ListPayments() []*Payment {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*Payment, 0, len(st.s.Payments))
	for _, p := range st.s.Payments {
		c := p.clone()
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
