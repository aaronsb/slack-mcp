package features

import (
	"slices"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/setup"
)

// A flow choice is hinted in the form the tool schema takes, action="select"
// with value=, not the flow's internal "select:<x>" spelling.
func TestAuthHintsASelectionInSchemaForm(t *testing.T) {
	res := flowToFeatureResult(&setup.FlowResponse{Actions: []string{"select:<browser_name>", "reset"}})
	want := []string{`auth action="select" value="<browser_name>"`, `auth action="reset"`}
	if !slices.Equal(res.NextActions, want) {
		t.Errorf("NextActions = %q, want %q", res.NextActions, want)
	}
}
