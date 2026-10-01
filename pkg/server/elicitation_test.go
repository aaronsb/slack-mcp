package server

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/aaronsb/slack-mcp/pkg/features"
)

// ADR-013, Elicitation: only a client on 2026-07-28 or later whose current
// request declares elicitation is asked in-band; every other client, and
// any SSE session, gets the CLI path.
func TestElicitationOnlyForModernRequestsThatDeclareIt(t *testing.T) {
	s := NewSemanticMCPServer(nil)
	elicit := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{}}
	modern := func(version string, caps *mcp.ClientCapabilities) context.Context {
		return server.WithRequestProtocolInfo(context.Background(), &server.RequestProtocolInfo{
			Modern: true, ProtocolVersion: version, ClientCapabilities: caps,
		})
	}
	var req mcp.CallToolRequest

	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"legacy request", context.Background(), false},
		{"modern without elicitation", modern(mcp.ProtocolVersion20260728, &mcp.ClientCapabilities{}), false},
		{"modern, no capabilities", modern(mcp.ProtocolVersion20260728, nil), false},
		{"modern with elicitation", modern(mcp.ProtocolVersion20260728, elicit), true},
	}
	for _, c := range cases {
		if got := s.elicitation(c.ctx, req).Offer; got != c.want {
			t.Errorf("%s: Offer = %v, want %v", c.name, got, c.want)
		}
	}

	req.Params.RequestState = "signed-state"
	req.Params.InputResponses = mcp.InputResponses{approvalKey: {Elicitation: &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionAccept, Content: map[string]any{"choice": "approve-once"}},
	}}}
	got := s.elicitation(modern(mcp.ProtocolVersion20260728, elicit), req)
	if !got.Answered || got.Action != "accept" || got.Choice != "approve-once" || got.State != "signed-state" {
		t.Fatalf("answer not carried: %+v", got)
	}

	s.ServeSSE("127.0.0.1:0")
	if s.elicitation(modern(mcp.ProtocolVersion20260728, elicit), req).Offer {
		t.Fatalf("an SSE session was offered elicitation")
	}
}

func TestApprovalFormIsInputRequired(t *testing.T) {
	res := approvalForm(&features.InputRequest{State: "st", Message: "Approve pending pabc?", Choices: []string{"approve-once", "deny"}})
	if res.ResultType != mcp.ResultTypeInputRequired || res.RequestState != "st" {
		t.Fatalf("result %+v", res)
	}
	in, ok := res.InputRequests[approvalKey]
	if !ok || in.Elicitation == nil || in.Elicitation.Message != "Approve pending pabc?" {
		t.Fatalf("input request %+v", res.InputRequests)
	}
}
