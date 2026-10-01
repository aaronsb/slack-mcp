//go:build unix

package safety

import (
	"syscall"
	"testing"
	"time"
)

// A FIFO at the file's path must be refused before it is opened, or the
// read would block with the store's mutex held.
func TestJournalRefusesFIFO(t *testing.T) {
	w := openTest(t, Strict)
	if err := syscall.Mkfifo(qpath(w), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan QuarantineState, 1)
	go func() { done <- w.Quarantine.State() }()
	select {
	case st := <-done:
		if st.Err == nil || !st.LockEngaged() {
			t.Fatalf("FIFO read as state: %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader blocked on a FIFO")
	}
	if _, err := w.Quarantine.RecordBlock(Block{Destination: channel("C1", "#a")}); err == nil {
		t.Fatal("wrote to a FIFO")
	}
}
