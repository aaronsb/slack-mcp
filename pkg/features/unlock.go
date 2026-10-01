package features

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/lifecycle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/scanner"
	"github.com/aaronsb/slack-mcp/pkg/setup"
	"github.com/aaronsb/slack-mcp/pkg/unlock"
)

// offerUnlock is the sentence a refusal and the server instructions carry
// so a desktop operator has a path to clear a lock (ADR-013, #132).
const offerUnlock = "When the operator asks to clear a lock, call unlock and give them the link; clearing is theirs to do on that page or with the slack-mcp CLI, never by any other route."

// Unlock opens ADR-013's local clearing page. It clears nothing itself and
// never reports what the operator cleared.
var Unlock = &Feature{
	Name:        "unlock",
	Description: "Open a page in the operator's browser where they can review and clear the outbound-safety locks: quarantined people and conversations, and the strike lock. Returns the page's link. The operator does the clearing there; this tool clears nothing and does not report what they clear. Call it when the operator asks to clear a lock; never open the link yourself.",
	Schema: map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	},
	Handler: unlockHandler,
}

var (
	unlockMu   sync.Mutex
	unlockPage *unlock.Instance
)

// startUnlock is the page's constructor; tests replace it.
var startUnlock = unlock.Start

func unlockHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	if dep, _ := lifecycle.DeploymentFromEnv(); dep == lifecycle.Remote {
		return &FeatureResult{
			Success: false,
			Message: "unlock is unavailable on a remote deployment: the page would open on the server's host, not the operator's. The operator clears locks with slack-mcp quarantine on that host.",
		}, nil
	}
	ap, _ := params["_provider"].(*provider.ApiProvider)
	if ap == nil {
		return &FeatureResult{Success: false, Message: "Internal error: provider not available."}, nil
	}
	ws, err := safetyFor(ap)
	if errors.Is(err, errNotIdentified) {
		return &FeatureResult{Success: false, Message: "The workspace is still connecting; call unlock again in a moment."}, nil
	}
	if err != nil {
		log.Printf("outbound-safety: unlock: state unavailable: %v", err)
		return &FeatureResult{Success: false, Message: "The outbound-safety state could not be opened, so there is no page to show. Tell the operator."}, nil
	}

	unlockMu.Lock()
	if unlockPage != nil {
		unlockPage.Stop()
		unlockPage = nil
	}
	in, err := startUnlock(ws.Quarantine, unlock.Options{
		Name:     func(k safety.Key) string { return keyName(ap, k) },
		Describe: func(r safety.Reason) string { return describeReason(ap, r) },
		Held: func() bool {
			held, _ := ws.UnrecordedHeld(time.Now())
			return held
		},
	})
	if err == nil {
		unlockPage = in
	}
	unlockMu.Unlock()
	if err != nil {
		log.Printf("outbound-safety: unlock: page not started: %v", err)
		return &FeatureResult{Success: false, Message: "The clearing page could not be started (no free local port). Tell the operator; slack-mcp quarantine clears from a terminal."}, nil
	}
	log.Printf("outbound-safety: clearing page opened on port %d", in.Port())
	setup.OpenBrowserURL(in.URL())

	return &FeatureResult{
		Success: true,
		Message: "Opened the clearing page in the operator's browser: " + in.URL(),
		Guidance: "If the browser did not open, give the operator this link. The page is theirs: they check what to clear and press Clear or Done, and either closes the page. " +
			"This result does not say what they cleared; your next say or mark-read runs every check again. Do not open the link yourself.",
	}, nil
}

// describeReason is a block's failure type for the page: the class, the
// field, where it was going, and when. Never the matched value, which is
// not recorded, and never an ID.
func describeReason(ap *provider.ApiProvider, r safety.Reason) string {
	class, ok := classNames[scanner.Class(r.Class)]
	if !ok {
		class = "a secret"
	}
	s := capitalize(class) + " in " + locationText(r.Location, r.File)
	// Recorded names are @handle or #name; anything else may be an ID.
	if d := r.Destination; strings.HasPrefix(d, "@") || strings.HasPrefix(d, "#") {
		s += ", sending to " + d
	}
	if !r.Time.IsZero() {
		s += ", " + r.Time.Local().Format("Jan 2 15:04")
	}
	return s
}

func locationText(loc string, file int) string {
	switch loc {
	case "reaction":
		return "the reaction"
	case "upload-title":
		return "the upload's title or comment"
	case "file-name":
		return fmt.Sprintf("the name of the %s attached file", ordinal(max(file, 1)))
	case "file-bytes":
		return fmt.Sprintf("the %s attached file", ordinal(max(file, 1)))
	default:
		return "the message text"
	}
}
