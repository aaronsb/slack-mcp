package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

// A test binary never falls back to the real home directory: an unset XDG
// variable panics, naming the variable to set.
func TestUnsetXDGPanicsInATest(t *testing.T) {
	for _, c := range []struct {
		env string
		dir func() string
	}{
		{"XDG_CONFIG_HOME", ConfigDir},
		{"XDG_DATA_HOME", DataDir},
		{"XDG_STATE_HOME", StateDir},
	} {
		t.Run(c.env, func(t *testing.T) {
			t.Setenv(c.env, "")
			defer func() {
				r := recover()
				if r == nil || !strings.Contains(r.(string), c.env) {
					t.Errorf("recovered %v, want a panic naming %s", r, c.env)
				}
			}()
			c.dir()
		})
	}
}

func TestSetXDGResolvesUnderIt(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)
	if got, want := DataDir(), filepath.Join(base, AppName); got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
}
