// Package unlock is ADR-013's local clearing page: a one-shot web page on
// the loopback address where the person at the keyboard reviews the
// quarantines and the strike lock and clears what they choose. The page
// clears; the tool that opens it does not.
package unlock

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/setup"
)

//go:embed page.html
var pageHTML string

var page = template.Must(template.New("unlock").Parse(pageHTML))

// IdleTimeout stops an instance nobody answered.
const IdleTimeout = 15 * time.Minute

// Store is the quarantine state the page reads and clears.
type Store interface {
	State() safety.QuarantineState
	Clear(target safety.Key, by, pendingID string, now time.Time) (bool, error)
}

// Options are what the caller supplies: how to name a key and describe a
// reason for a person, never by ID.
type Options struct {
	Name     func(safety.Key) string
	Describe func(safety.Reason) string
	// Held reports the server's in-memory hold after a block the file
	// could not record; the strike row then shows even with no strikes.
	Held func() bool
	Now  func() time.Time
	Idle time.Duration
	// Listen returns the port and listener; setup.FindPort by default.
	Listen func() (int, net.Listener, error)
}

// Instance is one running page. It serves one answer, Clear or Done, and
// stops.
type Instance struct {
	store Store
	opts  Options
	token string
	local *setup.LocalServer

	mu     sync.Mutex
	closed bool
	rows   []row
	done   chan struct{}
	timer  *time.Timer
}

type row struct {
	Key     safety.Key
	Index   int
	Title   string
	Scope   string
	Reasons []string
}

// Start serves a new page and returns it. The caller opens URL.
func Start(store Store, opts Options) (*Instance, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Idle == 0 {
		opts.Idle = IdleTimeout
	}
	if opts.Listen == nil {
		opts.Listen = setup.FindPort
	}
	if opts.Name == nil {
		opts.Name = func(k safety.Key) string { return k.Name }
	}
	if opts.Held == nil {
		opts.Held = func() bool { return false }
	}
	if opts.Describe == nil {
		opts.Describe = func(r safety.Reason) string { return r.Class }
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	port, listener, err := opts.Listen()
	if err != nil {
		return nil, err
	}
	in := &Instance{store: store, opts: opts, token: hex.EncodeToString(b[:]), done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/unlock/"+in.token, in.serve)
	in.local = setup.NewLocalServer(listener, port, mux)
	in.mu.Lock()
	in.timer = time.AfterFunc(opts.Idle, in.Stop)
	in.mu.Unlock()
	in.local.Start()
	return in, nil
}

// URL is the page's link, token included.
func (in *Instance) URL() string { return in.local.BaseURL() + "/unlock/" + in.token }

// Port is the loopback port the page listens on.
func (in *Instance) Port() int { return in.local.Port() }

// Done is closed when the instance stops.
func (in *Instance) Done() <-chan struct{} { return in.done }

// Stop closes the page; later requests fail. Safe to call more than once.
func (in *Instance) Stop() {
	in.mu.Lock()
	first := !in.closed
	in.closed = true
	in.timer.Stop()
	in.mu.Unlock()
	if first {
		close(in.done)
	}
	go in.local.Stop()
}

type view struct {
	Unreadable string
	Lock       *row
	Rows       []row
	Malformed  int
	Result     string
}

func (in *Instance) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")

	in.mu.Lock()
	defer in.mu.Unlock()
	if in.closed {
		http.Error(w, "This page is closed. Ask for a new link to clear anything else.", http.StatusGone)
		return
	}
	switch r.Method {
	case http.MethodGet:
		in.render(w, in.list())
	case http.MethodPost:
		if !setup.IsLoopbackOrigin(r.Header.Get("Origin"), in.local.Port()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		v := view{Result: in.answer(r.PostForm)}
		in.closed = true
		in.timer.Stop()
		close(in.done)
		in.render(w, v)
		go in.local.Stop()
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// list reads the state and keeps the rows the form's indexes refer to.
func (in *Instance) list() view {
	st := in.store.State()
	in.rows = nil
	var v view
	if st.Err != nil {
		v.Unreadable = "The safety state cannot be read, so every message and read receipt is refused. Nothing here can clear it until the state is readable again; running slack-mcp quarantine list in a terminal names the error."
		return v
	}
	v.Malformed = len(st.Malformed)
	held := in.opts.Held()
	if st.Strikes > 0 || st.LockEngaged() || held {
		title := fmt.Sprintf("Strikes: %d of %d", st.Strikes, st.Limit)
		scope := "Another block engages the strike lock."
		switch {
		case st.LockEngaged():
			title = fmt.Sprintf("Strike lock engaged (%d of %d)", st.Strikes, st.Limit)
			scope = "Every message and read receipt is refused, to anyone."
		case held:
			title = "Writes held: a block could not be recorded"
			scope = "Every message and read receipt is refused, to anyone. Clearing the strikes releases the hold unless the safety state was replaced or unreadable since; then approve the pending lift request with slack-mcp approve, or restart the server."
		}
		lr := row{Key: safety.StrikesKey, Title: title, Scope: scope}
		for _, rs := range st.StrikeReasons {
			lr.Reasons = append(lr.Reasons, in.opts.Describe(rs))
		}
		in.rows = append(in.rows, lr)
	}
	for _, k := range st.People {
		in.rows = append(in.rows, in.keyRow(st, k, "Every DM and group DM with them is closed to messages and read receipts."))
	}
	for _, k := range st.Conversations {
		in.rows = append(in.rows, in.keyRow(st, k, "Closed to messages and read receipts."))
	}
	for i := range in.rows {
		in.rows[i].Index = i
	}
	if len(in.rows) > 0 && in.rows[0].Key.Kind == safety.KeyStrikes {
		v.Lock = &in.rows[0]
		v.Rows = in.rows[1:]
	} else {
		v.Rows = in.rows
	}
	return v
}

func (in *Instance) keyRow(st safety.QuarantineState, k safety.Key, scope string) row {
	rw := row{Key: k, Title: in.opts.Name(k), Scope: scope}
	if rs, ok := st.ReasonFor(k); ok {
		rw.Reasons = []string{in.opts.Describe(rs)}
	}
	return rw
}

// answer applies the form: Clear clears the checked rows, Done nothing.
func (in *Instance) answer(form map[string][]string) string {
	if first(form["action"]) != "clear" {
		log.Printf("outbound-safety: clearing page closed without clearing")
		return "Closed without clearing anything. You can close this tab."
	}
	checked, cleared, failed := 0, 0, 0
	seen := map[int]bool{}
	for _, raw := range form["row"] {
		i, err := strconv.Atoi(raw)
		if err != nil || i < 0 || i >= len(in.rows) || seen[i] {
			continue
		}
		seen[i] = true
		checked++
		k := in.rows[i].Key
		ok, err := in.store.Clear(k, safety.ByWeb, "", in.opts.Now())
		switch {
		case err != nil:
			failed++
			log.Printf("outbound-safety: CLEAR %s %s failed by=web: %v", k.Kind, k.ID, err)
		case ok || k.Kind == safety.KeyStrikes:
			cleared++
			log.Printf("outbound-safety: CLEARED %s %s (%s) by=web", k.Kind, k.Name, k.ID)
		}
	}
	msg := fmt.Sprintf("Cleared %d.", cleared)
	switch {
	case checked == 0:
		msg = "Nothing was checked, so nothing was cleared."
	case cleared == 0 && failed == 0:
		msg = "Nothing needed clearing: what you checked was already clear."
	}
	if failed > 0 {
		msg += fmt.Sprintf(" %d could not be cleared; running slack-mcp quarantine list in a terminal shows why.", failed)
	}
	return msg + " This page is closed; you can close this tab."
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func (in *Instance) render(w http.ResponseWriter, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(w, v); err != nil {
		log.Printf("unlock: render: %v", err)
	}
}
