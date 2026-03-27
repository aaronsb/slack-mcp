package setup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BrowserInfo describes a detected browser installation
type BrowserInfo struct {
	Name        string `json:"name"`         // "chrome", "chromium", "edge", "firefox"
	DisplayName string `json:"display_name"` // "Google Chrome"
	ExePath     string `json:"exe_path"`
	UserDataDir string `json:"user_data_dir"`
	Type        string `json:"type"` // "chromium" or "firefox"
}

// ProfileInfo describes a Chrome/Chromium/Edge profile
type ProfileInfo struct {
	DirName     string `json:"dir_name"`     // "Default", "Profile 1"
	DisplayName string `json:"display_name"` // "Aaron"
	Email       string `json:"email,omitempty"`
}

// browserCandidate pairs an executable search with its metadata
type browserCandidate struct {
	name        string
	displayName string
	browserType string
	exePaths    []string // checked in order; first existing path wins
	userDataDir string
}

// DetectBrowsers finds installed browsers on the system.
// All filesystem checks, no network calls.
func DetectBrowsers() []BrowserInfo {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	candidates := browserCandidates(home)
	var browsers []BrowserInfo

	for _, c := range candidates {
		exePath := findExecutable(c.exePaths)
		if exePath == "" {
			continue
		}
		// Only include if user data dir exists (indicates browser has been used)
		if c.userDataDir != "" {
			if _, err := os.Stat(c.userDataDir); err != nil {
				continue
			}
		}
		browsers = append(browsers, BrowserInfo{
			Name:        c.name,
			DisplayName: c.displayName,
			ExePath:     exePath,
			UserDataDir: c.userDataDir,
			Type:        c.browserType,
		})
	}

	return browsers
}

// browserCandidates returns platform-specific browser search paths
func browserCandidates(home string) []browserCandidate {
	switch runtime.GOOS {
	case "linux":
		return []browserCandidate{
			{
				name: "chrome", displayName: "Google Chrome", browserType: "chromium",
				exePaths: []string{
					"/usr/bin/google-chrome-stable",
					"/usr/bin/google-chrome",
				},
				userDataDir: filepath.Join(home, ".config", "google-chrome"),
			},
			{
				name: "chromium", displayName: "Chromium", browserType: "chromium",
				exePaths: []string{
					"/usr/bin/chromium",
					"/usr/bin/chromium-browser",
					"/snap/bin/chromium",
				},
				userDataDir: filepath.Join(home, ".config", "chromium"),
			},
			{
				name: "edge", displayName: "Microsoft Edge", browserType: "chromium",
				exePaths: []string{
					"/usr/bin/microsoft-edge",
					"/usr/bin/microsoft-edge-stable",
				},
				userDataDir: filepath.Join(home, ".config", "microsoft-edge"),
			},
			{
				name: "firefox", displayName: "Firefox", browserType: "firefox",
				exePaths: []string{
					"/usr/bin/firefox",
					"/snap/bin/firefox",
				},
				userDataDir: filepath.Join(home, ".mozilla", "firefox"),
			},
		}
	case "darwin":
		return []browserCandidate{
			{
				name: "chrome", displayName: "Google Chrome", browserType: "chromium",
				exePaths: []string{
					"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
				},
				userDataDir: filepath.Join(home, "Library", "Application Support", "Google", "Chrome"),
			},
			{
				name: "chromium", displayName: "Chromium", browserType: "chromium",
				exePaths: []string{
					"/Applications/Chromium.app/Contents/MacOS/Chromium",
				},
				userDataDir: filepath.Join(home, "Library", "Application Support", "Chromium"),
			},
			{
				name: "edge", displayName: "Microsoft Edge", browserType: "chromium",
				exePaths: []string{
					"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
				},
				userDataDir: filepath.Join(home, "Library", "Application Support", "Microsoft Edge"),
			},
			{
				name: "firefox", displayName: "Firefox", browserType: "firefox",
				exePaths: []string{
					"/Applications/Firefox.app/Contents/MacOS/firefox",
				},
				userDataDir: filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles"),
			},
		}
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		programFiles := os.Getenv("ProgramFiles")
		programFilesX86 := os.Getenv("ProgramFiles(x86)")
		appData := os.Getenv("APPDATA")

		return []browserCandidate{
			{
				name: "chrome", displayName: "Google Chrome", browserType: "chromium",
				exePaths: []string{
					filepath.Join(programFiles, "Google", "Chrome", "Application", "chrome.exe"),
					filepath.Join(programFilesX86, "Google", "Chrome", "Application", "chrome.exe"),
					filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
				},
				userDataDir: filepath.Join(localAppData, "Google", "Chrome", "User Data"),
			},
			{
				name: "edge", displayName: "Microsoft Edge", browserType: "chromium",
				exePaths: []string{
					filepath.Join(programFiles, "Microsoft", "Edge", "Application", "msedge.exe"),
					filepath.Join(programFilesX86, "Microsoft", "Edge", "Application", "msedge.exe"),
				},
				userDataDir: filepath.Join(localAppData, "Microsoft", "Edge", "User Data"),
			},
			{
				name: "firefox", displayName: "Firefox", browserType: "firefox",
				exePaths: []string{
					filepath.Join(programFiles, "Mozilla Firefox", "firefox.exe"),
					filepath.Join(programFilesX86, "Mozilla Firefox", "firefox.exe"),
				},
				userDataDir: filepath.Join(appData, "Mozilla", "Firefox", "Profiles"),
			},
		}
	default:
		return nil
	}
}

// findExecutable returns the first path that exists and is executable, or
// falls back to exec.LookPath for short names.
func findExecutable(paths []string) string {
	for _, p := range paths {
		if filepath.IsAbs(p) {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p
			}
		} else {
			if resolved, err := exec.LookPath(p); err == nil {
				return resolved
			}
		}
	}
	return ""
}

// EnumerateProfiles reads Chrome/Chromium/Edge profile metadata from the
// "Local State" file in the given user data directory.
func EnumerateProfiles(userDataDir string) ([]ProfileInfo, error) {
	localStatePath := filepath.Join(userDataDir, "Local State")
	data, err := os.ReadFile(localStatePath)
	if err != nil {
		return nil, err
	}

	var localState struct {
		Profile struct {
			InfoCache map[string]struct {
				Name     string `json:"name"`
				GaiaName string `json:"gaia_name"`
				UserName string `json:"user_name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}

	if err := json.Unmarshal(data, &localState); err != nil {
		return nil, err
	}

	var profiles []ProfileInfo
	for dirName, info := range localState.Profile.InfoCache {
		displayName := info.Name
		if displayName == "" {
			displayName = info.GaiaName
		}
		if displayName == "" {
			displayName = dirName
		}

		profiles = append(profiles, ProfileInfo{
			DirName:     dirName,
			DisplayName: displayName,
			Email:       info.UserName,
		})
	}

	return profiles, nil
}

// IsProfileLocked checks whether a Chromium user data directory is currently
// locked by a running browser instance.
func IsProfileLocked(userDataDir string) bool {
	// Linux/macOS: SingletonLock is a symlink created by Chromium
	lockPath := filepath.Join(userDataDir, "SingletonLock")
	if _, err := os.Lstat(lockPath); err == nil {
		return true
	}

	// Windows: lockfile in the user data dir
	if runtime.GOOS == "windows" {
		lockfile := filepath.Join(userDataDir, "lockfile")
		if _, err := os.Stat(lockfile); err == nil {
			return true
		}
	}

	return false
}

// FindFirefoxExecutable returns the path to Firefox if installed
func FindFirefoxExecutable() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := browserCandidates(home)
	for _, c := range candidates {
		if c.browserType == "firefox" {
			if p := findExecutable(c.exePaths); p != "" {
				return p
			}
		}
	}
	return ""
}

// FilterChromium returns only Chromium-based browsers from the list
func FilterChromium(browsers []BrowserInfo) []BrowserInfo {
	var result []BrowserInfo
	for _, b := range browsers {
		if b.Type == "chromium" {
			result = append(result, b)
		}
	}
	return result
}

// FilterFirefox returns only Firefox browsers from the list
func FilterFirefox(browsers []BrowserInfo) []BrowserInfo {
	var result []BrowserInfo
	for _, b := range browsers {
		if b.Type == "firefox" {
			result = append(result, b)
		}
	}
	return result
}

// BrowserByName finds a browser by its short name (e.g., "chrome", "edge")
func BrowserByName(browsers []BrowserInfo, name string) *BrowserInfo {
	name = strings.ToLower(name)
	for i := range browsers {
		if browsers[i].Name == name {
			return &browsers[i]
		}
	}
	return nil
}
