package setup

import (
	"os"
	"testing"
)

// TestMain stops the setup flow from opening a real browser.
//
// Advancing the flow to StateManualFlow calls OpenBrowserURL, so running the
// package's tests used to spawn a browser tab per test that got that far —
// `make check` left a pile of them behind, and on a headless runner it is a
// pointless subprocess. Set here rather than in each test so a new test cannot
// forget.
func TestMain(m *testing.M) {
	os.Setenv(NoBrowserEnv, "1")
	os.Exit(m.Run())
}
