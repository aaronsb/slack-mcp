package setup

import (
	"fmt"
	"time"
)

// --- Flow lifecycle helpers ---

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

// --- State rendering for Status() ---

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
