package safety

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// RequestStateTTL is how long an elicitation's request state verifies.
const RequestStateTTL = 10 * time.Minute

// Choice is an answer an approval form offers.
type Choice string

const (
	ChoiceApproveOnce  Choice = "approve-once"
	ChoiceApproveTrust Choice = "approve-and-trust"
	ChoiceDeny         Choice = "deny"
)

// ChoicesFor is what the form offers under a posture: approve and trust
// only in soft.
func ChoicesFor(p Posture) []Choice {
	if p == Soft {
		return []Choice{ChoiceApproveOnce, ChoiceApproveTrust, ChoiceDeny}
	}
	return []Choice{ChoiceApproveOnce, ChoiceDeny}
}

// RequestState is what the server hands the client with an input-required
// result and gets back on the retry. It binds the destination, the content
// hash, the gate cases, the pending ID, the choices offered, and an expiry.
type RequestState struct {
	PendingID     string    `json:"id"`
	DestinationID string    `json:"dest"`
	ContentHash   string    `json:"hash"`
	Cases         []Case    `json:"cases"`
	Choices       []Choice  `json:"choices"`
	Expires       time.Time `json:"exp"`
	Nonce         string    `json:"nonce"`
}

// Offers reports whether the form offered c.
func (s RequestState) Offers(c Choice) bool {
	for _, x := range s.Choices {
		if x == c {
			return true
		}
	}
	return false
}

// Errors from Verify. Each counts as no answer: the request stays pending
// for the CLI.
var (
	ErrStateMalformed = errors.New("request state malformed")
	ErrStateTampered  = errors.New("request state signature does not verify")
	ErrStateExpired   = errors.New("request state expired")
	ErrStateMismatch  = errors.New("request state is for another call")
	ErrStateReplayed  = errors.New("request state already used")
)

// Signer signs and verifies request state under a key held in memory for
// the life of the process, so state from before a restart fails.
type Signer struct {
	key []byte

	mu   sync.Mutex
	used map[string]time.Time // nonce -> expiry
}

// NewSigner draws a fresh random key.
func NewSigner() (*Signer, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &Signer{key: key, used: map[string]time.Time{}}, nil
}

// Sign issues request state for a pending request, expiring
// RequestStateTTL after now.
func (s *Signer) Sign(r Request, choices []Choice, now time.Time) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	st := RequestState{
		PendingID:     r.ID,
		DestinationID: r.Destination.BindingID(),
		ContentHash:   r.ContentHash,
		Cases:         normCases(r.Cases),
		Choices:       choices,
		Expires:       now.Add(RequestStateTTL).UTC(),
		Nonce:         hex.EncodeToString(nonce),
	}
	payload, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(s.mac(payload)), nil
}

func (s *Signer) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("slack-mcp request state v1\x00"))
	m.Write(payload)
	return m.Sum(nil)
}

// Verify checks a returned state against the call it came back on: the
// signature, the expiry, and the binding (pending ID, destination, content
// hash, cases) and the answer given: a choice the form did not offer is
// ErrStateMismatch. A state verifies once; a second presentation is
// ErrStateReplayed. The pending request's own consumption is the second
// guard: a replayed state finds nothing to approve.
func (s *Signer) Verify(token, pendingID string, b Binding, choice Choice, now time.Time) (RequestState, error) {
	enc := base64.RawURLEncoding
	p, m, ok := strings.Cut(token, ".")
	if !ok {
		return RequestState{}, ErrStateMalformed
	}
	payload, err := enc.DecodeString(p)
	if err != nil {
		return RequestState{}, ErrStateMalformed
	}
	sig, err := enc.DecodeString(m)
	if err != nil {
		return RequestState{}, ErrStateMalformed
	}
	if !hmac.Equal(sig, s.mac(payload)) {
		return RequestState{}, ErrStateTampered
	}
	var st RequestState
	if err := json.Unmarshal(payload, &st); err != nil {
		return RequestState{}, ErrStateMalformed
	}
	if !now.Before(st.Expires) {
		return RequestState{}, ErrStateExpired
	}
	if st.PendingID != pendingID || st.DestinationID != b.DestinationID ||
		st.ContentHash != b.ContentHash || !sameCases(st.Cases, b.Cases) || !st.Offers(choice) {
		return RequestState{}, ErrStateMismatch
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for n, exp := range s.used {
		if !now.Before(exp) {
			delete(s.used, n)
		}
	}
	if _, seen := s.used[st.Nonce]; seen {
		return RequestState{}, ErrStateReplayed
	}
	s.used[st.Nonce] = st.Expires
	return st, nil
}
