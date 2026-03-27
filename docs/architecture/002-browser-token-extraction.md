# ADR-002: Browser-Automated Token Extraction

## Status

Draft

## Context

ADR-001 established a setup flow where users extract Slack session tokens (xoxc/xoxd) by opening DevTools, pasting a JavaScript snippet, navigating to the Application tab to copy the `d` cookie, then calling `send()` in the Console. This works but requires 7 manual steps and DevTools familiarity — a significant barrier for non-technical users and a friction point even for technical ones.

The current flow is also difficult for AI agents to guide, since the agent has no visibility into what the user sees in DevTools and cannot verify progress.

We want to reduce token extraction to near-zero manual steps when possible, while maintaining the current manual flow as a universal fallback.

### Constraints

- **Go binary is the primary artifact** — solutions must work from the compiled binary, not require a separate runtime for the common case.
- **Cross-platform** — must support Linux, macOS, and Windows.
- **Multi-browser** — Chrome/Chromium/Edge (dominant) and Firefox (meaningful minority). Safari is out of scope.
- **Existing authenticated sessions only** — the user has already completed a Slack login in their browser. We are here to peel off a copy of the tokens from that session, not to drive a new login. If we can't access the existing session, we fall through to a less-automated tier — we never prompt the user to log in.
- **npm wrapper exists** — Node.js is available in the npm distribution path, which opens additional options.
- **MCP tool is the primary interface** — the agent invokes the setup flow and guides the user conversationally. The tool must support progressive disclosure, not dump all instructions at registration time.

### Approaches Considered and Rejected

**Direct cookie/storage file extraction** (read browser SQLite databases from disk): Chrome encrypts cookies on all platforms using OS-specific key storage (Keychain, DPAPI, libsecret). The decryption logic is platform-specific, brittle, and breaks when Chrome updates its encryption scheme. Firefox cookies are less encrypted but localStorage uses a different storage format. The maintenance burden is not justified.

**Persistent browser extension**: Presents a permanent attack surface, requires store distribution or manual install, and browser extension review processes add friction and delay. Users would reasonably distrust installing a persistent extension that reads cookies and localStorage.

**Playwright via npm sidecar**: Requires downloading browser binaries (150-400MB per browser), only works in the npm distribution path, adds significant install time. Overkill for reading two values from an already-authenticated session.

## Decision

### Three-tier extraction strategy

The setup flow attempts strategies in order of automation, falling through on failure:

```
Tier 1: CDP (Chrome/Chromium/Edge)  — fully automatic
Tier 2: Temporary extension (Firefox) — one manual step
Tier 3: Manual flow (any browser)    — current behavior, always available
```

### Tier 1: Chrome DevTools Protocol via go-rod/rod

Use [rod](https://github.com/go-rod/rod), a Go library for Chrome DevTools Protocol, to control a Chromium-based browser and extract tokens programmatically.

Rod's `launcher.NewUserMode()` is purpose-built for this: it reuses the user's existing browser profile via `LookPath()` discovery, a fixed debug port, and no auto-download. It requires the browser to be fully closed (profile lock), which aligns with our extraction-only model.

**When Chrome is not running** (profile unlocked):
1. Use `launcher.LookPath()` to find an installed Chromium-based browser (Chrome, Chromium, Edge). If none found, skip to Tier 2.
2. Parse `Local State` JSON to enumerate profiles. If multiple profiles exist, present them to the agent with display names/emails for user selection.
3. Launch with `NewUserMode()` using the selected profile — headful, existing user data, no Chromium download.
4. Navigate to `app.slack.com`.
5. Extract xoxc token from localStorage and d cookie via CDP.
6. Validate tokens against `auth.test`.
7. Close the browser.

**When Chrome is running** (profile locked):
1. Detect the lock, inform the agent.
2. Ask the user to close all Chrome windows — Chrome restores tabs on relaunch, so this is non-destructive.
3. Agent polls with `retry` until the profile is unlocked.
4. If the user declines or can't close Chrome, fall through to Tier 2 (Firefox) or Tier 3 (manual).

We do not launch a fresh temporary profile. The goal is to extract tokens from an existing authenticated session, not drive a new login. If we can't access the session, we fall through.

Rod is included as an unconditional dependency. The binary size increase (~8MB, mostly the launcher package and its dependencies) is justified by the UX improvement and the unpredictable timing of when auth setup is needed.

### Network isolation constraint

The binary must produce zero outbound network traffic until the user explicitly initiates a Slack operation. This is a provable property of the source:

1. **No browser auto-download.** We never call rod's `browser.Get()` or allow the default `launcher.New()` path that triggers `fetchup`. All browser access goes through a wrapper function that requires an explicit filesystem path from `launcher.LookPath()` or user override. A `go vet`-style check or grep for `browser.Get()` and bare `launcher.New().Launch()` (without `.Bin()`) can enforce this at CI time.

2. **Browser detection is filesystem-only.** `launcher.LookPath()` checks known paths and `$PATH` — no network calls. Profile enumeration reads `Local State` JSON from disk.

3. **The only outbound calls are to `slack.com/api/*`** — `auth.test` during token validation, and the normal Slack API during operation. Both require user-provided tokens and happen only after explicit user action.

4. **The localhost callback server binds to `127.0.0.1` only** — no external listeners.

5. **Rod's embedded `leakless` guardian binary** is compiled into the Go binary at build time — no runtime download.

This means anyone auditing network traffic will see exactly one destination: Slack. The constraint is enforced structurally (wrapper function, no bare launcher calls) rather than by documentation alone.

### Tier 2: Temporary WebExtension for Firefox

A minimal WebExtension (manifest.json + content script, ~30 lines total) embedded in the Go binary via `go:embed`.

Flow:
1. Go binary writes the extension files to a temporary directory.
2. Opens Firefox to `about:debugging#/runtime/this-firefox`.
3. Agent guides user to click "Load Temporary Add-on" and select the manifest.json.
4. Extension activates on `app.slack.com`, reads localStorage for xoxc token and `document.cookie` for the d cookie.
5. Extension POSTs tokens to the localhost callback server (same endpoint as current manual flow).
6. Tokens are validated and saved.
7. Temporary extensions are automatically discarded when Firefox closes — no cleanup needed, no persistent attack surface.

### Tier 3: Manual flow (unchanged)

The existing embedded HTML setup page with console snippet and manual form. Always available as a fallback, and the default when no supported browser is detected.

### State machine with config-persisted flow state

The setup flow is a state machine that persists its current state in the existing config file (`~/.config/slack-mcp/config.json`) under a `setup_flow` key. This enables:

- **Resume across sessions** — if the MCP server restarts or the agent session ends mid-flow, the next `auth-setup` call picks up where the user left off.
- **Idempotent actions** — calling `next` in the same state returns the same response. `status` never mutates.
- **Reset** — clears the `setup_flow` key and any temporary resources (launched browsers, temp extension directories).
- **TTL** — flows older than 1 hour auto-expire to prevent stale state from confusing future sessions.

States:

```
idle → detecting → browser_choice → profile_scan → profile_choice
  → cdp_connect → [ok] → extracting → validating → complete
               → [locked] → prompt_close → retry → cdp_connect
                                        → fallthrough (next tier)
  → firefox_ext_written → waiting_for_callback → extracting
  → manual_flow → waiting_for_callback → extracting
```

### Progressive disclosure via response guidance

The MCP tool registration stays minimal: one tool, two-sentence description. All step-by-step guidance is embedded in each state's response, not in the tool schema.

Every state returns a uniform envelope:

```go
type FlowResponse struct {
    State    string         `json:"state"`
    Message  string         `json:"message"`   // what happened
    Guidance string         `json:"guidance"`   // what to tell the user / do next
    Actions  []string       `json:"actions"`    // valid next actions
    Context  map[string]any `json:"context"`    // state-specific data (profiles, browser info, etc.)
    Done     bool           `json:"done"`
    OK       bool           `json:"ok"`
}
```

The `guidance` field tells the agent exactly what to communicate to the user at each step. The `actions` field constrains what the agent can do next. The agent never needs to know the full state graph — it just follows the guidance and picks from available actions.

### Browser and profile detection

**Browser discovery**: Platform-specific known paths for Chrome, Chromium, and Edge. Check existence, not PATH lookup, to find all installed Chromium variants.

**Profile enumeration**: Parse `Local State` JSON at the browser's user-data-dir root. The `profile.info_cache` key maps profile directory names to display names and email addresses. This works identically on all platforms — only the user-data-dir root path differs.

**Slack signal detection**: Optionally scan profile history or cookie DB filenames for `app.slack.com` references to pre-filter profiles likely to have Slack sessions. This is a hint, not a requirement — the user confirms the profile choice.

### File structure

```
pkg/setup/
├── flow.go              # State machine, FlowResponse, state transitions
├── flow_detect.go       # Browser + profile detection
├── flow_cdp.go          # Rod: launch, connect, extract tokens
├── flow_firefox.go      # Write temp extension, guide loading
├── flow_firefox_ext/    # go:embed
│   ├── manifest.json
│   └── content.js
├── server.go            # Existing localhost callback (shared by all tiers)
├── config.go            # Config persistence (gains setup_flow field)
└── page.html            # Existing manual flow page (Tier 3)
```

## Consequences

### Positive

- Chrome/Edge users (majority) get fully automatic token extraction — zero DevTools interaction.
- Firefox users get a single manual step (load temp extension) instead of seven.
- Manual flow preserved as universal fallback — no user is worse off.
- State machine enables reliable agent-guided flows across session boundaries.
- Progressive disclosure keeps MCP tool registration lightweight.
- Rod is pure Go with no CGO — cross-compilation remains trivial.
- Temporary Firefox extension has no persistent attack surface.

### Negative

- Rod adds ~8MB to the binary (launcher package, crypto deps, embedded leakless guardian). The actual CDP protocol layer is only ~100KB of that.
- Rod's download machinery (`fetchup`, browser snapshot fetching) is still compiled into the binary even though we never call it. The dead code is inert but visible to auditors — we mitigate with a CI check that greps for forbidden call sites.
- Profile lock detection and the "close Chrome" flow adds UX complexity for a common case (most users have Chrome running). The fallthrough to lower tiers mitigates this — users are never stuck.
- Firefox temp extension flow still requires manual action — the gap between Tier 1 and Tier 2 UX is notable.
- State machine adds code complexity to what was previously a simple HTTP callback flow.

### Risks

- Chrome profile format (`Local State` JSON) could change, breaking profile enumeration.
- Rod's browser detection heuristics may not cover all Chromium variants (Brave, Vivaldi, Arc). We should support a manual browser path override.
- Firefox's `about:debugging` UI could change, breaking the agent's guidance text. The guidance should describe the action generically ("load a temporary add-on") rather than reference specific UI elements.
