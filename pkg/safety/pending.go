package safety

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// PendingTTL is how long a request lives, approved or not (ADR-013).
const PendingTTL = 24 * time.Hour

// Status is where a pending request stands.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved" // by the CLI, awaiting the next matching call
	StatusDenied   Status = "denied"
	StatusConsumed Status = "consumed" // one send let through, or a lift applied
	StatusExpired  Status = "expired"
)

// Who answered a request.
const (
	AnswerCLI         = "cli"
	AnswerElicitation = "elicitation"
)

// Request is a gated call waiting on the operator.
type Request struct {
	ID          string      `json:"id"`
	Created     time.Time   `json:"created"`
	Expires     time.Time   `json:"expires"`
	Cases       []Case      `json:"cases"`
	Tool        string      `json:"tool"`
	Destination Destination `json:"destination"`
	// ContentHash binds the request to the content (HashContent).
	ContentHash string `json:"content_hash,omitempty"`
	FileCount   int    `json:"file_count,omitempty"`
	// From names the conversations a moved file came from (case 2).
	From []string `json:"from,omitempty"`
	// Text and FileNames are held for case 1 only, so the operator can read
	// what would be sent, and are gone once the request expires.
	Text      string   `json:"text,omitempty"`
	FileNames []string `json:"file_names,omitempty"`
	// Lift names what a case 3 request lifts: people, conversations, or
	// StrikesKey.
	Lift []Key `json:"lift,omitempty"`

	Status     Status    `json:"-"`
	Resolved   time.Time `json:"-"`
	ResolvedBy string    `json:"-"`
}

// IsLift reports whether this is a case 3 request.
func (r Request) IsLift() bool { return hasCase(r.Cases, CaseLift) }

// Binding is what a later call must match to use a request: the
// destination, the content hash, and the set of gate cases. DestinationID is
// Destination.BindingID(): a person or one-to-one DM by the person's user
// ID, so a request issued before the DM was opened still matches after.
// BindingFor builds one.
type Binding struct {
	DestinationID string
	ContentHash   string
	Cases         []Case
}

// BindingFor is the binding of a call to d with this content and cases.
func BindingFor(d Destination, contentHash string, cases []Case) Binding {
	return Binding{DestinationID: d.BindingID(), ContentHash: contentHash, Cases: normCases(cases)}
}

func (r Request) matches(b Binding) bool {
	return r.Destination.BindingID() == b.DestinationID && r.ContentHash == b.ContentHash && sameCases(r.Cases, b.Cases)
}

// sameCall is the repeat rule: a repeat of the same call while its request
// is pending names the same ID. A lift is the same call when it lifts the
// same keys, whatever its content.
func (r Request) sameCall(o Request) bool {
	if r.Destination.BindingID() != o.Destination.BindingID() || !sameCases(r.Cases, o.Cases) {
		return false
	}
	if r.IsLift() {
		return sameKeys(r.Lift, o.Lift)
	}
	return r.ContentHash == o.ContentHash
}

func sameKeys(a, b []Key) bool {
	set := func(ks []Key) map[string]bool {
		m := map[string]bool{}
		for _, k := range ks {
			m[slotKey(k)] = true
		}
		return m
	}
	sa, sb := set(a), set(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if !sb[k] {
			return false
		}
	}
	return true
}

// Pending-file kinds.
const (
	kindRequest = "request"
	kindApprove = "approve"
	kindDeny    = "deny"
	kindConsume = "consume"
)

type pendingLine struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Request *Request  `json:"request,omitempty"`
	ID      string    `json:"id,omitempty"`
	By      string    `json:"by,omitempty"`
}

// Errors from answering a request.
var (
	ErrNoRequest  = errors.New("no such request (it may have expired)")
	ErrNotPending = errors.New("request is no longer pending")
	ErrMismatch   = errors.New("request does not match this call")
)

// PendingStore is one workspace's pending-request file and its fold.
type PendingStore struct {
	mu    sync.Mutex
	j     *journal
	reqs  map[string]*Request
	order []string
	raw   map[string][][]byte // each request's lines, for compaction
}

func newPendingStore(path string) *PendingStore {
	s := &PendingStore{j: newJournal(path)}
	s.reset()
	return s
}

func (s *PendingStore) reset() {
	s.reqs = map[string]*Request{}
	s.order = nil
	s.raw = map[string][][]byte{}
}

func (s *PendingStore) apply(raw []byte) error {
	var l pendingLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return err
	}
	keep := func(id string) { s.raw[id] = append(s.raw[id], append([]byte(nil), raw...)) }
	if l.Kind == kindRequest {
		if l.Request == nil || l.Request.ID == "" {
			return errors.New("request line without a request")
		}
		r := *l.Request
		r.Status = StatusPending
		if _, dup := s.reqs[r.ID]; !dup {
			s.order = append(s.order, r.ID)
		}
		s.reqs[r.ID] = &r
		s.raw[r.ID] = nil
		keep(r.ID)
		return nil
	}
	r, ok := s.reqs[l.ID]
	if !ok {
		return fmt.Errorf("%s for unknown request %q", l.Kind, l.ID)
	}
	open := r.Status == StatusPending || r.Status == StatusApproved
	switch l.Kind {
	case kindApprove:
		if r.Status == StatusPending {
			r.Status = StatusApproved
			if r.IsLift() {
				r.Status = StatusConsumed // a lift is applied when approved
			}
		}
	case kindDeny:
		if open {
			r.Status = StatusDenied
		}
	case kindConsume:
		if open {
			r.Status = StatusConsumed
		}
	default:
		return fmt.Errorf("unknown kind %q", l.Kind)
	}
	if open {
		r.Resolved, r.ResolvedBy = l.Time, l.By
	}
	keep(l.ID)
	return nil
}

func (s *PendingStore) refreshLocked() { s.j.refresh(s.reset, s.apply) }

// view returns a copy of r as of now: expired when its time ran out while
// open, with case 1 content dropped once expired.
func view(r *Request, now time.Time) Request {
	out := *r
	out.Cases = append([]Case(nil), r.Cases...)
	if (out.Status == StatusPending || out.Status == StatusApproved) && !now.Before(out.Expires) {
		out.Status = StatusExpired
	}
	if !now.Before(out.Expires) {
		out.Text, out.FileNames = "", nil
	}
	return out
}

// Err reports a pending file that exists but cannot be read.
func (s *PendingStore) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.j.err
}

// Malformed lists the 1-based line numbers skipped as unparseable.
func (s *PendingStore) Malformed() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return s.j.malformedLines()
}

// Lookup returns request id as of now.
func (s *PendingStore) Lookup(id string, now time.Time) (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	r, ok := s.reqs[id]
	if !ok {
		return Request{}, false
	}
	return view(r, now), true
}

// List returns every open request (pending, or approved and not yet used)
// that has not expired, oldest first: what `slack-mcp approve` lists.
func (s *PendingStore) List(now time.Time) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	var out []Request
	for _, id := range s.order {
		v := view(s.reqs[id], now)
		if v.Status == StatusPending || v.Status == StatusApproved {
			out = append(out, v)
		}
	}
	return out
}

// Create issues a request for a gated call and returns it, with created
// false when a pending request for the same call already exists (its ID is
// named again; the caller logs PENDING only when created). Content is held
// only for case 1. A request expires PendingTTL after issue; Create first
// drops every expired request, with its content, from the file.
//
// Order at the gate: call ConsumeApproved first, and Create only when it
// finds no approval. Create does not look at approved requests, so a gate
// that calls Create first issues a fresh pending request for a call the
// operator already approved (the approval stays unused until
// ConsumeApproved is called).
func (s *PendingStore) Create(req Request, now time.Time) (Request, bool, error) {
	req.Cases = normCases(req.Cases)
	if len(req.Cases) == 0 {
		return Request{}, false, errors.New("request needs a gate case")
	}
	if req.IsLift() {
		if len(req.Cases) != 1 || len(req.Lift) == 0 {
			return Request{}, false, errors.New("a lift request lifts something and is gated for nothing else")
		}
	}
	if !hasCase(req.Cases, CaseExternal) {
		req.Text, req.FileNames = "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Request
	created := false
	err := s.j.withLock(func() error {
		s.refreshLocked()
		if s.j.err != nil {
			return fmt.Errorf("pending requests cannot be read: %w", s.j.err)
		}
		if _, err := s.expireLocked(now); err != nil {
			return err
		}
		for _, id := range s.order {
			v := view(s.reqs[id], now)
			if v.Status == StatusPending && v.sameCall(req) {
				out = v
				return nil
			}
		}
		id, err := s.newID()
		if err != nil {
			return err
		}
		req.ID = id
		req.Created = now.UTC()
		req.Expires = req.Created.Add(PendingTTL)
		r := req
		raw, err := json.Marshal(pendingLine{Time: req.Created, Kind: kindRequest, Request: &r})
		if err != nil {
			return err
		}
		if err := s.j.appendLine(raw); err != nil {
			return err
		}
		s.refreshLocked()
		out = view(s.reqs[id], now)
		created = true
		return nil
	})
	return out, created, err
}

const idAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// newID returns "p" and three characters, unused in the file.
func (s *PendingStore) newID() (string, error) {
	for range 1000 {
		b := []byte{'p', 0, 0, 0}
		for i := 1; i < len(b); i++ {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(idAlphabet))))
			if err != nil {
				return "", err
			}
			b[i] = idAlphabet[n.Int64()]
		}
		if _, used := s.reqs[string(b)]; !used {
			return string(b), nil
		}
	}
	return "", errors.New("no free request ID")
}

// resolve appends kind for id when the request is open and, when check is
// set, passes it.
func (s *PendingStore) resolve(id, kind, by string, now time.Time, check func(Request) error) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Request
	err := s.j.withLock(func() error {
		s.refreshLocked()
		if s.j.err != nil {
			return fmt.Errorf("pending requests cannot be read: %w", s.j.err)
		}
		r, ok := s.reqs[id]
		if !ok {
			return ErrNoRequest
		}
		v := view(r, now)
		if v.Status != StatusPending && v.Status != StatusApproved {
			return fmt.Errorf("%w: %s", ErrNotPending, v.Status)
		}
		if check != nil {
			if err := check(v); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(pendingLine{Time: now.UTC(), Kind: kind, ID: id, By: by})
		if err != nil {
			return err
		}
		if err := s.j.appendLine(raw); err != nil {
			return err
		}
		s.refreshLocked()
		out = view(s.reqs[id], now)
		return nil
	})
	return out, err
}

// Deny marks a request denied. A repeat of the call then issues a new one.
func (s *PendingStore) Deny(id, by string, now time.Time) (Request, error) {
	return s.resolve(id, kindDeny, by, now, nil)
}

// Consume uses request id for one send: the elicitation path, where the
// current call proceeds. The call must match the request's binding, and the
// request must be open and unexpired; when two calls race, the first wins
// and the others get ErrNotPending.
func (s *PendingStore) Consume(id string, b Binding, by string, now time.Time) (Request, error) {
	return s.resolve(id, kindConsume, by, now, func(r Request) error {
		if r.IsLift() {
			return errors.New("a lift request lets no call through")
		}
		if !r.matches(b) {
			return ErrMismatch
		}
		return nil
	})
}

// ConsumeApproved finds a CLI-approved request matching the call and uses
// it for this one send. ok is false when there is none.
func (s *PendingStore) ConsumeApproved(b Binding, now time.Time) (Request, bool, error) {
	s.mu.Lock()
	s.refreshLocked()
	var id string
	for _, rid := range s.order {
		v := view(s.reqs[rid], now)
		if v.Status == StatusApproved && !v.IsLift() && v.matches(b) {
			id = rid
			break
		}
	}
	s.mu.Unlock()
	if id == "" {
		return Request{}, false, nil
	}
	r, err := s.resolve(id, kindConsume, AnswerCLI, now, func(r Request) error {
		if r.Status != StatusApproved || !r.matches(b) {
			return ErrNotPending
		}
		return nil
	})
	if errors.Is(err, ErrNotPending) || errors.Is(err, ErrNoRequest) {
		return Request{}, false, nil // another call won the race
	}
	if err != nil {
		return Request{}, false, err
	}
	return r, true, nil
}

// Expire drops every request whose 24 hours have run out, with the content
// held for it, by rewriting the file without them. It returns how many were
// dropped. Create runs it on every issue, and the CLI on every approve and
// deny, so held content does not outlive its request for long.
func (s *PendingStore) Expire(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	err := s.j.withLock(func() error {
		s.refreshLocked()
		if s.j.err != nil {
			return fmt.Errorf("pending requests cannot be read: %w", s.j.err)
		}
		var err error
		dropped, err = s.expireLocked(now)
		return err
	})
	return dropped, err
}

// expireLocked does Expire's work; the caller holds the mutex and the lock
// and has refreshed.
func (s *PendingStore) expireLocked(now time.Time) (int, error) {
	dropped := 0
	var keep [][]byte
	for _, id := range s.order {
		if !now.Before(s.reqs[id].Expires) {
			dropped++
			continue
		}
		keep = append(keep, s.raw[id]...)
	}
	if dropped == 0 {
		return 0, nil
	}
	if err := s.j.rewrite(keep); err != nil {
		return 0, err
	}
	s.refreshLocked()
	return dropped, nil
}
