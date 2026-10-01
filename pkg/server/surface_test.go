package server

import (
	"sort"
	"strings"
	"testing"
)

// The advertised surface: ADR-009's eight tools, ADR-010's batch, ADR-013's
// unlock, and ADR-012's put. A tool added or dropped fails here first.
func TestRegisteredSurfaceIsExactlyElevenTools(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := NewSemanticMCPServer(nil)

	var got []string
	for name := range s.server.ListTools() {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"auth", "batch", "dismiss", "download", "estate", "inbox", "mark-read", "messages", "put", "say", "unlock"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("registered tools:\n got %v\nwant %v", got, want)
	}
}
