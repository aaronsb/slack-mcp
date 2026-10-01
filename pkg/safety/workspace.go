package safety

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/paths"
)

// File names in a workspace's directory.
const (
	QuarantineFile = "quarantine.jsonl"
	TrustFile      = "trust.jsonl"
	PendingFile    = "pending.jsonl"
)

// Workspace is one workspace's safety state: the quarantine, trust, and
// pending-request files, read under one posture.
type Workspace struct {
	TeamID     string
	Dir        string
	Posture    Posture
	Quarantine *QuarantineStore
	Trust      *TrustStore
	Pending    *PendingStore
}

// Dir is the workspace's directory: the estate ledger's, keyed by team ID
// (estate.Open), in the data directory.
func Dir(teamID string) string {
	return filepath.Join(paths.DataDir(), "ledger", pathElement(teamID))
}

// pathElement matches the estate package's per-team directory name, so the
// safety files sit beside the estate ledger (a test holds the two equal).
func pathElement(name string) string {
	sum := sha256.Sum256([]byte(name))
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		cleaned = "unnamed"
	}
	if len(cleaned) > 48 {
		cleaned = cleaned[:48]
	}
	return cleaned + "-" + hex.EncodeToString(sum[:4])
}

// Open returns the safety state for a team under a posture. It creates the
// directory (0700) but no file: a missing file is empty state.
func Open(teamID string, p Posture) (*Workspace, error) {
	if teamID == "" {
		return nil, errors.New("safety: empty team ID")
	}
	return OpenDir(Dir(teamID), teamID, p)
}

// OpenDir opens the state in dir. Open is the production entry point.
func OpenDir(dir, teamID string, p Posture) (*Workspace, error) {
	if p != Strict && p != Soft {
		return nil, fmt.Errorf("safety: unknown posture %q", p)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("safety: create dir: %w", err)
	}
	return &Workspace{
		TeamID:     teamID,
		Dir:        dir,
		Posture:    p,
		Quarantine: newQuarantineStore(filepath.Join(dir, QuarantineFile), p),
		Trust:      newTrustStore(filepath.Join(dir, TrustFile), p),
		Pending:    newPendingStore(filepath.Join(dir, PendingFile)),
	}, nil
}

// Approve answers a pending request from the CLI. For case 1 or 2 it marks
// the request approved, and the next call with the same destination and
// content goes through once (ConsumeApproved). For case 3 it appends a clear
// for each lifted key and lets nothing through.
func (w *Workspace) Approve(id string, now time.Time) (Request, error) {
	r, ok := w.Pending.Lookup(id, now)
	if !ok {
		return Request{}, ErrNoRequest
	}
	if r.Status != StatusPending {
		return r, fmt.Errorf("%w: %s", ErrNotPending, r.Status)
	}
	if r.IsLift() {
		for _, k := range r.Lift {
			if _, err := w.Quarantine.Clear(k, ByApproval, id, now); err != nil {
				return r, err
			}
		}
	}
	return w.Pending.approve(id, AnswerCLI, now)
}

func dirOf(path string) string { return filepath.Dir(path) }
