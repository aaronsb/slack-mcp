package safety

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Identity is whose account the session tokens belong to (ADR-014).
type Identity string

const (
	// Human: the account belongs to a person; the agent writes for them.
	Human Identity = "human"
	// Agent: the account is the agent's own.
	Agent Identity = "agent"
)

// Posture is how hard a block escalates (ADR-014).
type Posture string

const (
	// Strict: quarantine on the first block, strike lock at two. The
	// default, and the posture for an unattended agent.
	Strict Posture = "strict"
	// Soft: warn on the first block, quarantine from the second, strike
	// lock at three. The attended posture.
	Soft Posture = "soft"
)

// Environment variables. Neither is on the .env allowlist: both come from
// the MCP client config only.
const (
	IdentityEnv = "SLACK_MCP_IDENTITY"
	SafetyEnv   = "SLACK_MCP_SAFETY"
)

// StrikeLimit is the strike count at which the posture engages the lock.
func StrikeLimit(p Posture) int {
	if p == Soft {
		return 3
	}
	return 2
}

// Settings are the two declared settings, read once at startup.
type Settings struct {
	Identity Identity
	Posture  Posture
}

// DefaultSettings is what an unset environment means: a person's account,
// strict posture. Both defaults are the more cautious value.
var DefaultSettings = Settings{Identity: Human, Posture: Strict}

// ParseIdentity reads a SLACK_MCP_IDENTITY value. Empty is the default; an
// unknown value is an error naming the setting and its allowed values.
func ParseIdentity(raw string) (Identity, error) {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case "":
		return Human, nil
	case string(Human), string(Agent):
		return Identity(v), nil
	default:
		return "", fmt.Errorf("%s=%q: want human or agent", IdentityEnv, v)
	}
}

// ParsePosture reads a SLACK_MCP_SAFETY value, as ParseIdentity does.
func ParsePosture(raw string) (Posture, error) {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case "":
		return Strict, nil
	case string(Strict), string(Soft):
		return Posture(v), nil
	default:
		return "", fmt.Errorf("%s=%q: want strict or soft", SafetyEnv, v)
	}
}

// SettingsFromEnv reads both settings through lookup (os.LookupEnv at
// startup). The caller refuses to start on an error, as it does for
// SLACK_MCP_DEPLOYMENT.
func SettingsFromEnv(lookup func(string) (string, bool)) (Settings, error) {
	idRaw, _ := lookup(IdentityEnv)
	id, err := ParseIdentity(idRaw)
	if err != nil {
		return Settings{}, err
	}
	pRaw, _ := lookup(SafetyEnv)
	p, err := ParsePosture(pRaw)
	if err != nil {
		return Settings{}, err
	}
	return Settings{Identity: id, Posture: p}, nil
}

var current atomic.Pointer[Settings]

// SetCurrent records the settings read at startup.
func SetCurrent(s Settings) { current.Store(&s) }

// Current returns the settings recorded at startup, or DefaultSettings
// when none were.
func Current() Settings {
	if s := current.Load(); s != nil {
		return *s
	}
	return DefaultSettings
}
