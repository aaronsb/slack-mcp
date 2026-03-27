package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFlowStateExpiry(t *testing.T) {
	tests := []struct {
		name     string
		state    *FlowState
		expected bool
	}{
		{"nil state", nil, true},
		{"fresh state", &FlowState{StartedAt: time.Now()}, false},
		{"expired state", &FlowState{StartedAt: time.Now().Add(-2 * time.Hour)}, true},
		{"just under TTL", &FlowState{StartedAt: time.Now().Add(-59 * time.Minute)}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.FlowExpired(); got != tt.expected {
				t.Errorf("FlowExpired() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFlowStateMachineTransitions(t *testing.T) {
	// Use a temp config dir so tests don't touch real config
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	f, err := NewFlow()
	if err != nil {
		t.Fatalf("NewFlow() error: %v", err)
	}

	// Should start idle
	s := f.Status()
	if s.State != StateIdle {
		t.Fatalf("initial state = %q, want %q", s.State, StateIdle)
	}
	if len(s.Actions) == 0 || s.Actions[0] != "next" {
		t.Fatalf("initial actions = %v, want [next]", s.Actions)
	}

	// Reset from idle should stay idle
	r := f.Reset()
	if r.State != StateIdle {
		t.Fatalf("reset state = %q, want %q", r.State, StateIdle)
	}

	// Invalid action should return error message
	r = f.Advance("bogus")
	if r.State != StateIdle {
		t.Fatalf("bogus action state = %q, want %q", r.State, StateIdle)
	}
	if r.Message == "" {
		t.Fatal("bogus action should have a message")
	}
}

func TestFlowDetectNoBrowsers(t *testing.T) {
	// On a system with no browsers in expected paths (like CI), detection
	// should fall through to manual flow
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HOME", t.TempDir()) // No real browser paths

	f, err := NewFlow()
	if err != nil {
		t.Fatalf("NewFlow() error: %v", err)
	}

	r := f.Advance("next")
	// Should either show browsers or fall to manual — both are valid
	validStates := map[string]bool{
		StateBrowserChoice:      true,
		StateManualFlow:         true,
		StateWaitingForCallback: true,
	}
	if !validStates[r.State] {
		t.Fatalf("after detect, state = %q, want one of %v", r.State, validStates)
	}
}

func TestFlowConfigPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Save a flow state
	cfg := &Config{
		Workspaces: make(map[string]WorkspaceConfig),
		SetupFlow: &FlowState{
			State:     StateBrowserChoice,
			StartedAt: time.Now(),
		},
	}
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}

	// Load it back
	loaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if loaded.SetupFlow == nil {
		t.Fatal("SetupFlow should not be nil after load")
	}
	if loaded.SetupFlow.State != StateBrowserChoice {
		t.Fatalf("SetupFlow.State = %q, want %q", loaded.SetupFlow.State, StateBrowserChoice)
	}

	// Clear it
	if err := loaded.ClearFlow(); err != nil {
		t.Fatalf("ClearFlow error: %v", err)
	}
	loaded2, _ := LoadConfig()
	if loaded2.SetupFlow != nil {
		t.Fatal("SetupFlow should be nil after ClearFlow")
	}
}

func TestProfileEnumeration(t *testing.T) {
	// Create a fake Local State file
	tmpDir := t.TempDir()
	localState := map[string]interface{}{
		"profile": map[string]interface{}{
			"info_cache": map[string]interface{}{
				"Default": map[string]interface{}{
					"name":      "Alice",
					"user_name": "alice@example.com",
				},
				"Profile 1": map[string]interface{}{
					"name":      "Bob",
					"gaia_name": "Bob G",
					"user_name": "bob@example.com",
				},
			},
		},
	}

	data, _ := json.MarshalIndent(localState, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "Local State"), data, 0644); err != nil {
		t.Fatalf("failed to write Local State: %v", err)
	}

	profiles, err := EnumerateProfiles(tmpDir)
	if err != nil {
		t.Fatalf("EnumerateProfiles error: %v", err)
	}

	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}

	// Check that both profiles are found (order is map-iteration-order, not guaranteed)
	found := map[string]bool{}
	for _, p := range profiles {
		found[p.DirName] = true
		if p.DirName == "Default" {
			if p.DisplayName != "Alice" {
				t.Errorf("Default display name = %q, want Alice", p.DisplayName)
			}
			if p.Email != "alice@example.com" {
				t.Errorf("Default email = %q, want alice@example.com", p.Email)
			}
		}
	}
	if !found["Default"] || !found["Profile 1"] {
		t.Errorf("missing profiles: %v", found)
	}
}

func TestProfileEnumerationMissingFile(t *testing.T) {
	_, err := EnumerateProfiles(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing Local State")
	}
}

func TestIsProfileLocked(t *testing.T) {
	tmpDir := t.TempDir()

	// No lock file — should be unlocked
	if IsProfileLocked(tmpDir) {
		t.Fatal("expected unlocked with no SingletonLock")
	}

	// Create a SingletonLock symlink (like Chrome does on Linux)
	lockPath := filepath.Join(tmpDir, "SingletonLock")
	os.Symlink("hostname-12345", lockPath)

	if !IsProfileLocked(tmpDir) {
		t.Fatal("expected locked with SingletonLock present")
	}
}

func TestBrowserByName(t *testing.T) {
	browsers := []BrowserInfo{
		{Name: "chrome", DisplayName: "Google Chrome", Type: "chromium"},
		{Name: "firefox", DisplayName: "Firefox", Type: "firefox"},
	}

	if b := BrowserByName(browsers, "chrome"); b == nil || b.Name != "chrome" {
		t.Error("expected to find chrome")
	}
	if b := BrowserByName(browsers, "CHROME"); b == nil || b.Name != "chrome" {
		t.Error("expected case-insensitive match")
	}
	if b := BrowserByName(browsers, "safari"); b != nil {
		t.Error("expected nil for missing browser")
	}
}

func TestFilterBrowsers(t *testing.T) {
	browsers := []BrowserInfo{
		{Name: "chrome", Type: "chromium"},
		{Name: "edge", Type: "chromium"},
		{Name: "firefox", Type: "firefox"},
	}

	chromium := FilterChromium(browsers)
	if len(chromium) != 2 {
		t.Errorf("FilterChromium got %d, want 2", len(chromium))
	}

	firefox := FilterFirefox(browsers)
	if len(firefox) != 1 {
		t.Errorf("FilterFirefox got %d, want 1", len(firefox))
	}
}

func TestCallbackServerTokenReceive(t *testing.T) {
	port, listener, err := FindPort()
	if err != nil {
		t.Fatalf("FindPort error: %v", err)
	}

	cs := NewCallbackServer(listener, port)
	cs.Start()
	defer cs.Stop()

	// Initially no result
	if r := cs.Result(); r != nil {
		t.Fatal("expected nil result before callback")
	}

	if cs.Port() != port {
		t.Errorf("Port() = %d, want %d", cs.Port(), port)
	}
}
