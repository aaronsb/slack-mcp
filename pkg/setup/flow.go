package setup

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

// Flow states
const (
	StateIdle               = "idle"
	StateDetecting          = "detecting"
	StateBrowserChoice      = "browser_choice"
	StateProfileScan        = "profile_scan"
	StateProfileChoice      = "profile_choice"
	StateCDPConnect         = "cdp_connect"
	StateProfileLocked      = "profile_locked"
	StateExtracting         = "extracting"
	StateValidating         = "validating"
	StateComplete           = "complete"
	StateFailed             = "failed"
	StateFirefoxExtWritten  = "firefox_ext_written"
	StateWaitingForCallback = "waiting_for_callback"
	StateManualFlow         = "manual_flow"
)

// FlowResponse is the uniform envelope returned by every state transition.
type FlowResponse struct {
	State    string         `json:"state"`
	Message  string         `json:"message"`
	Guidance string         `json:"guidance"`
	Actions  []string       `json:"actions"`
	Context  map[string]any `json:"context,omitempty"`
	Done     bool           `json:"done"`
	OK       bool           `json:"ok"`
}

// Flow manages the browser token extraction state machine.
type Flow struct {
	mu sync.Mutex

	state    string
	cfg      *Config
	browsers []BrowserInfo
	profiles []ProfileInfo

	// Selected browser/profile for CDP
	selectedBrowser *BrowserInfo
	selectedProfile string

	// Callback server for Firefox/manual tiers
	callback *CallbackServer
	listener net.Listener
	port     int

	// CDP extractor (async)
	cdpExtractor *CDPExtractor

	// Firefox extension temp dir
	tempDir string
}

// NewFlow creates or resumes a setup flow. If a non-expired flow state exists
// in config, it resumes from that state.
func NewFlow() (*Flow, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	f := &Flow{
		cfg:   cfg,
		state: StateIdle,
	}

	// Resume from persisted state if valid
	if cfg.SetupFlow != nil && !cfg.SetupFlow.FlowExpired() {
		f.state = cfg.SetupFlow.State
		f.tempDir = cfg.SetupFlow.TempDir
		f.port = cfg.SetupFlow.Port
	} else if cfg.SetupFlow != nil {
		// Expired — clean up
		cfg.ClearFlow()
	}

	return f, nil
}

// Advance moves the flow forward. The action determines which transition to take.
// Non-interactive states auto-chain within a single call.
func (f *Flow) Advance(action string) *FlowResponse {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case f.state == StateIdle && action == "next":
		return f.doDetect()

	case f.state == StateBrowserChoice && strings.HasPrefix(action, "select:"):
		return f.doSelectBrowser(strings.TrimPrefix(action, "select:"))

	case f.state == StateProfileChoice && strings.HasPrefix(action, "select:"):
		return f.doSelectProfile(strings.TrimPrefix(action, "select:"))

	case f.state == StateProfileLocked && action == "retry":
		return f.doCDPConnect()

	case f.state == StateProfileLocked && action == "next":
		return f.doFallthrough()

	case f.state == StateFirefoxExtWritten && action == "next":
		return f.doStartCallbackWait()

	case f.state == StateManualFlow && action == "next":
		return f.doStartCallbackWait()

	case f.state == StateExtracting && action == "status":
		return f.doCheckCDP()

	case f.state == StateWaitingForCallback && action == "status":
		return f.doCheckCallback()

	case f.state == StateFailed && action == "retry":
		f.state = StateIdle
		f.persist()
		return f.doDetect()

	default:
		return &FlowResponse{
			State:   f.state,
			Message: fmt.Sprintf("Invalid action %q for state %q", action, f.state),
			Actions: f.actionsForState(),
		}
	}
}

// Status returns the current state without mutating anything.
func (f *Flow) Status() *FlowResponse {
	f.mu.Lock()
	defer f.mu.Unlock()

	// If extracting via CDP, check if done
	if f.state == StateExtracting && f.cdpExtractor != nil {
		if r := f.cdpExtractor.Result(); r != nil {
			f.cdpExtractor.Cleanup()
			f.cdpExtractor = nil
			return f.handleTokenResult(r)
		}
	}

	// If waiting for callback, check if it arrived
	if f.state == StateWaitingForCallback && f.callback != nil {
		if r := f.callback.Result(); r != nil {
			return f.handleTokenResult(r)
		}
	}

	return &FlowResponse{
		State:   f.state,
		Message: f.messageForState(),
		Actions: f.actionsForState(),
		Context: f.contextForState(),
	}
}

// Reset clears all flow state and temporary resources.
func (f *Flow) Reset() *FlowResponse {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cleanup()
	f.state = StateIdle
	f.browsers = nil
	f.profiles = nil
	f.selectedBrowser = nil
	f.selectedProfile = ""
	f.cfg.ClearFlow()

	return &FlowResponse{
		State:   StateIdle,
		Message: "Setup flow reset.",
		Actions: []string{"next"},
	}
}
