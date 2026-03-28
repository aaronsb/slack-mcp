package setup

import (
	"fmt"
	"time"
)

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
		return f.doDirectExtract()
	}

	f.profiles = profiles

	if len(profiles) == 1 {
		f.selectedProfile = profiles[0].DirName
		return f.doDirectExtract()
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
	return f.doDirectExtract()
}

func (f *Flow) doDirectExtract() *FlowResponse {
	f.state = StateExtracting
	f.persist()

	// Extract tokens directly from the browser's profile files.
	// No browser launch needed — reads localStorage (LevelDB) and
	// decrypts cookies (SQLite + system keyring).
	xoxc, xoxd, err := ExtractTokensDirectly(
		f.selectedBrowser.UserDataDir,
		f.selectedProfile,
	)
	if err != nil {
		f.state = StateFailed
		f.persist()
		return &FlowResponse{
			State:   StateFailed,
			Message: fmt.Sprintf("Token extraction failed: %v", err),
			Guidance: "Could not extract tokens from the browser profile. " +
				"Make sure you are logged into Slack in this browser profile. " +
				"Try a different browser or profile with 'reset'.",
			Actions: []string{"retry", "reset"},
		}
	}

	// Validate and save
	team, user, userID, vErr := ValidateTokens(xoxc, xoxd)
	if vErr != nil {
		f.state = StateFailed
		f.persist()
		return &FlowResponse{
			State:   StateFailed,
			Message: fmt.Sprintf("Tokens extracted but validation failed: %v", vErr),
			Guidance: "The tokens were read from the browser but Slack rejected them. " +
				"The session may have expired. Log into Slack in this browser profile and try again.",
			Actions: []string{"retry", "reset"},
		}
	}

	return f.handleTokenResult(&TokenResult{
		Xoxc: xoxc, Xoxd: xoxd,
		Team: team, User: user, UserID: userID,
	})
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

	// Save tokens to config
	f.cfg.Workspaces[r.Team] = WorkspaceConfig{
		XoxcToken: r.Xoxc,
		XoxdToken: r.Xoxd,
		TeamName:  r.Team,
		UserName:  r.User,
		UserID:    r.UserID,
	}
	if f.cfg.DefaultWorkspace == "" {
		f.cfg.DefaultWorkspace = r.Team
	}

	f.cleanup()
	f.state = StateComplete
	f.cfg.SetupFlow = nil
	if err := SaveConfig(f.cfg); err != nil {
		f.state = StateFailed
		return &FlowResponse{
			State:   StateFailed,
			Message: fmt.Sprintf("Tokens valid but failed to save config: %v", err),
			Actions: []string{"retry", "reset"},
		}
	}

	return &FlowResponse{
		State:   StateComplete,
		Message: fmt.Sprintf("Connected to **%s** as **%s**.", r.Team, r.User),
		Guidance: "Authentication is complete. All Slack tools are now ready to use. " +
			"Try check-unreads, check-mentions, or catch-up to get started.",
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
