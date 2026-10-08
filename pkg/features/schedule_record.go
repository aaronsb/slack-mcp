package features

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/safety"
)

// What this server scheduled (ADR-016): enough of each draft, as Slack
// stored it at creation, to tell later whether the person edited it in
// Slack. It reports; it never blocks. The file sits in the workspace's state
// directory beside the estate ledger and holds no message content, only a
// hash of the blocks.

const scheduledFile = "scheduled.json"

type scheduledRecord struct {
	LastUpdatedTS string `json:"last_updated_ts"`
	DateScheduled int64  `json:"date_scheduled"`
	BlocksSHA     string `json:"blocks_sha"`
}

var scheduledMu sync.Mutex

// scheduledPath is the record file for the provider's workspace, or "" when
// the workspace is not identified yet.
func scheduledPath(ap *provider.ApiProvider) string {
	id := ap.ProvideIdentity()
	if id == nil || id.TeamID == "" {
		return ""
	}
	return filepath.Join(safety.Dir(id.TeamID), scheduledFile)
}

func loadScheduled(path string) map[string]scheduledRecord {
	recs := map[string]scheduledRecord{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("schedule: record unreadable: %v", err)
		}
		return recs
	}
	if err := json.Unmarshal(raw, &recs); err != nil {
		log.Printf("schedule: record malformed, starting empty: %v", err)
		return map[string]scheduledRecord{}
	}
	return recs
}

func saveScheduled(path string, recs map[string]scheduledRecord) {
	raw, err := json.Marshal(recs)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, raw, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		log.Printf("schedule: record not saved: %v", err)
	}
}

// blocksSHA hashes blocks in a canonical form, so key order in Slack's JSON
// does not read as an edit.
func blocksSHA(raw json.RawMessage) string {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		v = string(raw)
	}
	canon, _ := json.Marshal(v)
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// recordScheduled notes a draft this server created. A failure is logged:
// the draft is scheduled either way, and would list as scheduled from Slack.
func recordScheduled(ap *provider.ApiProvider, d provider.Draft) {
	path := scheduledPath(ap)
	if path == "" || d.ID == "" {
		return
	}
	scheduledMu.Lock()
	defer scheduledMu.Unlock()
	recs := loadScheduled(path)
	recs[d.ID] = scheduledRecord{LastUpdatedTS: d.LastUpdatedTS, DateScheduled: d.DateScheduled, BlocksSHA: blocksSHA(d.Blocks)}
	saveScheduled(path, recs)
}

func scheduledRecords(ap *provider.ApiProvider) map[string]scheduledRecord {
	path := scheduledPath(ap)
	if path == "" {
		return map[string]scheduledRecord{}
	}
	scheduledMu.Lock()
	defer scheduledMu.Unlock()
	return loadScheduled(path)
}

// pruneScheduled drops records whose drafts are no longer pending. Call it
// only with a complete list.
func pruneScheduled(ap *provider.ApiProvider, active []provider.Draft) {
	path := scheduledPath(ap)
	if path == "" {
		return
	}
	live := map[string]bool{}
	for _, d := range active {
		if provider.IsScheduled(d) {
			live[d.ID] = true
		}
	}
	scheduledMu.Lock()
	defer scheduledMu.Unlock()
	recs := loadScheduled(path)
	changed := false
	for id := range recs {
		if !live[id] {
			delete(recs, id)
			changed = true
		}
	}
	if changed {
		saveScheduled(path, recs)
	}
}

func forgetScheduled(ap *provider.ApiProvider, id string) {
	path := scheduledPath(ap)
	if path == "" {
		return
	}
	scheduledMu.Lock()
	defer scheduledMu.Unlock()
	recs := loadScheduled(path)
	if _, ok := recs[id]; ok {
		delete(recs, id)
		saveScheduled(path, recs)
	}
}

// originOf says where a pending draft came from and whether it changed
// since this server scheduled it.
func originOf(recs map[string]scheduledRecord, d provider.Draft, z zones, now time.Time) string {
	r, ok := recs[d.ID]
	if !ok {
		return "scheduled from Slack"
	}
	if r.LastUpdatedTS == d.LastUpdatedTS {
		return "scheduled here"
	}
	var changes []string
	if r.DateScheduled != d.DateScheduled {
		changes = append(changes, "time moved from "+z.render(time.Unix(r.DateScheduled, 0), now))
	}
	if r.BlocksSHA != blocksSHA(d.Blocks) {
		changes = append(changes, "content changed")
	}
	if len(changes) == 0 {
		return "scheduled here; edited in Slack since, no change to time or content found"
	}
	s := "scheduled here; edited in Slack since: " + changes[0]
	for _, c := range changes[1:] {
		s += "; " + c
	}
	return s
}
