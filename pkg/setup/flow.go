package setup

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
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

// --- State transition implementations ---

func (f *Flow) doDetect() *FlowResponse {
	f.state = StateDetecting
	browsers := DetectBrowsers()
	f.browsers = browsers

	if len(browsers) == 0 {
		// No browsers found — go straight to manual
		return f.doManualFlow()
	}

	chromium := FilterChromium(browsers)
	firefox := FilterFirefox(browsers)

	if len(chromium) == 0 && len(firefox) == 0 {
		return f.doManualFlow()
	}

	// If only one Chromium browser and no Firefox, skip choice
	if len(chromium) == 1 && len(firefox) == 0 {
		f.selectedBrowser = &chromium[0]
		return f.doProfileScan()
	}

	// If only Firefox, skip to extension
	if len(chromium) == 0 && len(firefox) > 0 {
		f.selectedBrowser = &firefox[0]
		return f.doFirefoxExtension()
	}

	// Multiple options — let user choose
	f.state = StateBrowserChoice
	f.persist()

	browserList := make([]map[string]string, len(browsers))
	for i, b := range browsers {
		browserList[i] = map[string]string{
			"name":         b.Name,
			"display_name": b.DisplayName,
			"type":         b.Type,
		}
	}

	return &FlowResponse{
		State:    StateBrowserChoice,
		Message:  fmt.Sprintf("Found %d browser(s).", len(browsers)),
		Guidance: "Ask the user which browser has Slack logged in.",
		Actions:  []string{"select:<browser_name>", "reset"},
		Context:  map[string]any{"browsers": browserList},
	}
}

func (f *Flow) doSelectBrowser(name string) *FlowResponse {
	b := BrowserByName(f.browsers, name)
	if b == nil {
		return &FlowResponse{
			State:   StateBrowserChoice,
			Message: fmt.Sprintf("Browser %q not found.", name),
			Actions: []string{"select:<browser_name>", "reset"},
			Context: f.contextForState(),
		}
	}

	f.selectedBrowser = b

	if b.Type == "firefox" {
		return f.doFirefoxExtension()
	}

	return f.doProfileScan()
}

func (f *Flow) doProfileScan() *FlowResponse {
	f.state = StateProfileScan

	profiles, err := EnumerateProfiles(f.selectedBrowser.UserDataDir)
	if err != nil || len(profiles) == 0 {
		// Can't enumerate — try Default profile
		f.selectedProfile = "Default"
		return f.doCDPConnect()
	}

	f.profiles = profiles

	if len(profiles) == 1 {
		f.selectedProfile = profiles[0].DirName
		return f.doCDPConnect()
	}

	// Multiple profiles — let user choose
	f.state = StateProfileChoice
	f.persist()

	profileList := make([]map[string]string, len(profiles))
	for i, p := range profiles {
		entry := map[string]string{
			"dir_name":     p.DirName,
			"display_name": p.DisplayName,
		}
		if p.Email != "" {
			entry["email"] = p.Email
		}
		profileList[i] = entry
	}

	return &FlowResponse{
		State:    StateProfileChoice,
		Message:  fmt.Sprintf("Found %d profile(s) in %s.", len(profiles), f.selectedBrowser.DisplayName),
		Guidance: "Ask the user which profile has Slack logged in. Show them the profile names and email addresses.",
		Actions:  []string{"select:<dir_name>", "reset"},
		Context:  map[string]any{"profiles": profileList, "browser": f.selectedBrowser.DisplayName},
	}
}

func (f *Flow) doSelectProfile(dirName string) *FlowResponse {
	found := false
	for _, p := range f.profiles {
		if p.DirName == dirName {
			found = true
			break
		}
	}
	if !found {
		return &FlowResponse{
			State:   StateProfileChoice,
			Message: fmt.Sprintf("Profile %q not found.", dirName),
			Actions: []string{"select:<dir_name>", "reset"},
			Context: f.contextForState(),
		}
	}

	f.selectedProfile = dirName
	return f.doCDPConnect()
}

func (f *Flow) doCDPConnect() *FlowResponse {
	f.state = StateCDPConnect
	f.persist()

	// Check profile lock before attempting launch
	if IsProfileLocked(f.selectedBrowser.UserDataDir) {
		f.state = StateProfileLocked
		f.persist()

		return &FlowResponse{
			State:   StateProfileLocked,
			Message: fmt.Sprintf("%s is currently running — its profile is locked.", f.selectedBrowser.DisplayName),
			Guidance: fmt.Sprintf(
				"Tell the user: %s needs to be fully closed (not just minimized) to access their login session. "+
					"All tabs will restore when they reopen the browser. "+
					"Once closed, use 'retry'. Or use 'next' to try a different method.",
				f.selectedBrowser.DisplayName,
			),
			Actions: []string{"retry", "next", "reset"},
			Context: map[string]any{"browser": f.selectedBrowser.DisplayName},
		}
	}

	// Launch browser and start extraction in background
	ext, err := StartCDPExtraction(
		f.selectedBrowser.ExePath,
		f.selectedBrowser.UserDataDir,
		f.selectedProfile,
	)
	if err != nil {
		f.state = StateFailed
		f.persist()
		return &FlowResponse{
			State:    StateFailed,
			Message:  fmt.Sprintf("Failed to launch browser: %v", err),
			Guidance: "Could not start the browser for token extraction. Try a different browser or use 'retry' to start over.",
			Actions:  []string{"retry", "reset"},
		}
	}

	f.cdpExtractor = ext
	f.state = StateExtracting
	f.persist()

	return &FlowResponse{
		State:   StateExtracting,
		Message: fmt.Sprintf("%s is opening Slack. This may take a moment.", f.selectedBrowser.DisplayName),
		Guidance: "Tell the user: a browser window is opening and navigating to Slack. " +
			"Wait for Slack to fully load (you should see your workspace), then tell the agent. " +
			"Poll with 'status' to check if token extraction completed.",
		Actions: []string{"status", "reset"},
		Context: map[string]any{"browser": f.selectedBrowser.DisplayName},
	}
}

func (f *Flow) doCheckCDP() *FlowResponse {
	if f.cdpExtractor == nil {
		return &FlowResponse{
			State:   StateFailed,
			Message: "No CDP extraction in progress.",
			Actions: []string{"retry", "reset"},
		}
	}

	r := f.cdpExtractor.Result()
	if r == nil {
		return &FlowResponse{
			State:    StateExtracting,
			Message:  "Still extracting tokens — waiting for Slack to load...",
			Guidance: "The browser is still loading Slack. Ask the user if they can see their Slack workspace. Keep polling with 'status'.",
			Actions:  []string{"status", "reset"},
		}
	}

	// Extraction complete — clean up browser
	f.cdpExtractor.Cleanup()
	f.cdpExtractor = nil

	return f.handleTokenResult(r)
}

func (f *Flow) doFirefoxExtension() *FlowResponse {
	// Ensure callback server is running
	if err := f.ensureCallbackServer(); err != nil {
		f.state = StateFailed
		f.persist()
		return &FlowResponse{
			State:   StateFailed,
			Message: fmt.Sprintf("Failed to start callback server: %v", err),
			Actions: []string{"retry", "reset"},
		}
	}

	// Write extension to temp dir
	dir, err := WriteFirefoxExtension(f.port)
	if err != nil {
		f.state = StateFailed
		f.persist()
		return &FlowResponse{
			State:   StateFailed,
			Message: fmt.Sprintf("Failed to write Firefox extension: %v", err),
			Actions: []string{"retry", "reset"},
		}
	}

	f.tempDir = dir
	f.state = StateFirefoxExtWritten
	f.cfg.SetupFlow = &FlowState{
		State:     StateFirefoxExtWritten,
		TempDir:   dir,
		Port:      f.port,
		StartedAt: time.Now(),
	}
	SaveConfig(f.cfg)

	return &FlowResponse{
		State:   StateFirefoxExtWritten,
		Message: fmt.Sprintf("Firefox extension written to %s", dir),
		Guidance: fmt.Sprintf(
			"Guide the user through these steps:\n"+
				"1. Open Firefox and navigate to a Slack workspace (app.slack.com) — make sure they're logged in.\n"+
				"2. Open a new tab and go to about:debugging#/runtime/this-firefox\n"+
				"3. Click 'Load Temporary Add-on'\n"+
				"4. Navigate to %s and select manifest.json\n"+
				"5. Switch back to the Slack tab and reload the page.\n"+
				"The extension will automatically extract tokens and send them to our callback server.\n"+
				"Use 'next' to start waiting for the callback, then poll with 'status'.",
			dir,
		),
		Actions: []string{"next", "reset"},
		Context: map[string]any{
			"extension_dir": dir,
			"callback_port": f.port,
		},
	}
}

func (f *Flow) doManualFlow() *FlowResponse {
	// Ensure callback server is running
	if err := f.ensureCallbackServer(); err != nil {
		f.state = StateFailed
		f.persist()
		return &FlowResponse{
			State:   StateFailed,
			Message: fmt.Sprintf("Failed to start callback server: %v", err),
			Actions: []string{"retry", "reset"},
		}
	}

	f.state = StateManualFlow
	f.persist()

	url := fmt.Sprintf("http://localhost:%d", f.port)
	OpenBrowserURL(url)

	return &FlowResponse{
		State:    StateManualFlow,
		Message:  fmt.Sprintf("Manual setup page opened at %s", url),
		Guidance: "Tell the user a browser window opened with setup instructions. They'll need to open DevTools on a Slack tab to extract tokens. Use 'next' to start waiting, then poll with 'status'.",
		Actions:  []string{"next", "reset"},
		Context:  map[string]any{"url": url, "callback_port": f.port},
	}
}

func (f *Flow) doStartCallbackWait() *FlowResponse {
	f.state = StateWaitingForCallback
	f.persist()

	return &FlowResponse{
		State:    StateWaitingForCallback,
		Message:  "Waiting for tokens from browser...",
		Guidance: "Poll with 'status' to check if tokens have been received. The user is completing the browser flow.",
		Actions:  []string{"status", "reset"},
	}
}

func (f *Flow) doCheckCallback() *FlowResponse {
	if f.callback == nil {
		return &FlowResponse{
			State:   StateFailed,
			Message: "Callback server is not running.",
			Actions: []string{"retry", "reset"},
		}
	}

	r := f.callback.Result()
	if r == nil {
		return &FlowResponse{
			State:    StateWaitingForCallback,
			Message:  "Still waiting for tokens...",
			Guidance: "The user hasn't completed the browser flow yet. Keep polling with 'status'.",
			Actions:  []string{"status", "reset"},
		}
	}

	return f.handleTokenResult(r)
}

func (f *Flow) handleTokenResult(r *TokenResult) *FlowResponse {
	if r.Err != nil {
		f.state = StateFailed
		f.persist()
		return &FlowResponse{
			State:    StateFailed,
			Message:  fmt.Sprintf("Token extraction failed: %v", r.Err),
			Guidance: "Something went wrong. Try again with 'retry' or 'reset' to start fresh.",
			Actions:  []string{"retry", "reset"},
		}
	}

	f.cleanup()
	f.state = StateComplete
	f.cfg.ClearFlow()

	return &FlowResponse{
		State:   StateComplete,
		Message: fmt.Sprintf("Connected to %s as %s.", r.Team, r.User),
		Done:    true,
		OK:      true,
		Context: map[string]any{"team": r.Team, "user": r.User},
	}
}

func (f *Flow) doFallthrough() *FlowResponse {
	// Try Firefox if available
	firefox := FilterFirefox(f.browsers)
	if len(firefox) > 0 {
		f.selectedBrowser = &firefox[0]
		return f.doFirefoxExtension()
	}

	// Fall through to manual
	return f.doManualFlow()
}

// --- Helpers ---

func (f *Flow) saveTokens(xoxc, xoxd, team, user, userID string) *FlowResponse {
	f.cfg.Workspaces[team] = WorkspaceConfig{
		XoxcToken: xoxc,
		XoxdToken: xoxd,
		TeamName:  team,
		UserName:  user,
		UserID:    userID,
	}
	if f.cfg.DefaultWorkspace == "" {
		f.cfg.DefaultWorkspace = team
	}
	f.cfg.SetupFlow = nil
	if err := SaveConfig(f.cfg); err != nil {
		f.state = StateFailed
		return &FlowResponse{
			State:   StateFailed,
			Message: fmt.Sprintf("Tokens valid but failed to save config: %v", err),
			Actions: []string{"retry", "reset"},
		}
	}

	f.cleanup()
	f.state = StateComplete

	return &FlowResponse{
		State:   StateComplete,
		Message: fmt.Sprintf("Connected to %s as %s.", team, user),
		Done:    true,
		OK:      true,
		Context: map[string]any{"team": team, "user": user},
	}
}

func (f *Flow) ensureCallbackServer() error {
	if f.callback != nil {
		return nil
	}

	if f.listener == nil {
		port, listener, err := FindPort()
		if err != nil {
			return err
		}
		f.port = port
		f.listener = listener
	}

	f.callback = NewCallbackServer(f.listener, f.port)
	f.callback.Start()
	return nil
}

func (f *Flow) cleanup() {
	if f.cdpExtractor != nil {
		f.cdpExtractor.Cleanup()
		f.cdpExtractor = nil
	}
	if f.callback != nil {
		f.callback.Stop()
		f.callback = nil
	}
	if f.tempDir != "" {
		CleanupFirefoxExtension(f.tempDir)
		f.tempDir = ""
	}
	f.listener = nil
}

func (f *Flow) persist() {
	if f.cfg.SetupFlow == nil {
		f.cfg.SetupFlow = &FlowState{StartedAt: time.Now()}
	}
	f.cfg.SetupFlow.State = f.state
	if f.selectedBrowser != nil {
		f.cfg.SetupFlow.BrowserPath = f.selectedBrowser.ExePath
		f.cfg.SetupFlow.BrowserName = f.selectedBrowser.DisplayName
		f.cfg.SetupFlow.UserDataDir = f.selectedBrowser.UserDataDir
	}
	f.cfg.SetupFlow.ProfileDir = f.selectedProfile
	f.cfg.SetupFlow.TempDir = f.tempDir
	f.cfg.SetupFlow.Port = f.port
	SaveConfig(f.cfg)
}

func (f *Flow) actionsForState() []string {
	switch f.state {
	case StateIdle:
		return []string{"next"}
	case StateBrowserChoice:
		return []string{"select:<browser_name>", "reset"}
	case StateProfileChoice:
		return []string{"select:<dir_name>", "reset"}
	case StateProfileLocked:
		return []string{"retry", "next", "reset"}
	case StateExtracting:
		return []string{"status", "reset"}
	case StateFirefoxExtWritten, StateManualFlow:
		return []string{"next", "reset"}
	case StateWaitingForCallback:
		return []string{"status", "reset"}
	case StateFailed:
		return []string{"retry", "reset"}
	case StateComplete:
		return nil
	default:
		return []string{"reset"}
	}
}

func (f *Flow) messageForState() string {
	switch f.state {
	case StateIdle:
		return "Ready to start browser token extraction."
	case StateBrowserChoice:
		return fmt.Sprintf("Found %d browser(s). Waiting for selection.", len(f.browsers))
	case StateProfileChoice:
		return fmt.Sprintf("Found %d profile(s). Waiting for selection.", len(f.profiles))
	case StateProfileLocked:
		browserName := "Browser"
		if f.selectedBrowser != nil {
			browserName = f.selectedBrowser.DisplayName
		}
		return fmt.Sprintf("%s is running — profile is locked.", browserName)
	case StateFirefoxExtWritten:
		return "Firefox extension written. Waiting for user to load it."
	case StateWaitingForCallback:
		return "Waiting for tokens from browser..."
	case StateManualFlow:
		return "Manual setup page opened. Waiting for user to complete flow."
	case StateComplete:
		return "Setup complete."
	case StateFailed:
		return "Setup failed."
	default:
		return fmt.Sprintf("State: %s", f.state)
	}
}

func (f *Flow) contextForState() map[string]any {
	ctx := map[string]any{}

	switch f.state {
	case StateBrowserChoice:
		list := make([]map[string]string, len(f.browsers))
		for i, b := range f.browsers {
			list[i] = map[string]string{
				"name":         b.Name,
				"display_name": b.DisplayName,
				"type":         b.Type,
			}
		}
		ctx["browsers"] = list

	case StateProfileChoice:
		list := make([]map[string]string, len(f.profiles))
		for i, p := range f.profiles {
			entry := map[string]string{
				"dir_name":     p.DirName,
				"display_name": p.DisplayName,
			}
			if p.Email != "" {
				entry["email"] = p.Email
			}
			list[i] = entry
		}
		ctx["profiles"] = list

	case StateFirefoxExtWritten:
		ctx["extension_dir"] = f.tempDir
		ctx["callback_port"] = f.port

	case StateManualFlow, StateWaitingForCallback:
		if f.port > 0 {
			ctx["callback_port"] = f.port
		}
	}

	return ctx
}
