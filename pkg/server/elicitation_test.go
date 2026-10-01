package server

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
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
	// On SSE an answer carried on a retry is ignored too: no request state
	// is honored there.
	if got := s.elicitation(modern(mcp.ProtocolVersion20260728, elicit), req); got.Offer || got.Answered || got.State != "" {
		t.Fatalf("an SSE session was offered elicitation or had its answer read: %+v", got)
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

// Each distinct client answer is logged once, naming what decided the
// approval form, so a live check can see why a client was not offered it.
func TestClientApprovalCapabilityIsLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	s := NewSemanticMCPServer(nil)
	ctx := server.WithRequestProtocolInfo(context.Background(), &server.RequestProtocolInfo{
		Modern: true, ProtocolVersion: mcp.ProtocolVersion20260728,
		ClientInfo:         &mcp.Implementation{Name: "desk\nfake: line", Version: "1.0"},
		ClientCapabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{}},
	})
	var req mcp.CallToolRequest
	s.elicitation(ctx, req)
	s.elicitation(ctx, req)
	s.elicitation(context.Background(), req)

	out := buf.String()
	if n := strings.Count(out, "client: "); n != 2 {
		t.Fatalf("logged %d client lines, want one per distinct answer:\n%s", n, out)
	}
	if !strings.Contains(out, `name="desk\nfake: line" version="1.0" protocol="`+mcp.ProtocolVersion20260728+`" elicitation=true sse=false approval-form=true`) {
		t.Errorf("modern client line wrong or unquoted:\n%s", out)
	}
	if !strings.Contains(out, `protocol="legacy" elicitation=false sse=false approval-form=false`) {
		t.Errorf("legacy line missing:\n%s", out)
	}
}

// A legacy request's protocol version is the client's own string: it is
// quoted so a newline cannot forge a line, and distinct client answers are
// capped so a client varying them cannot grow memory or the log.
func TestClientLogLinesAreQuotedAndCapped(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	s := NewSemanticMCPServer(nil)
	var req mcp.CallToolRequest
	forged := server.WithRequestProtocolInfo(context.Background(), &server.RequestProtocolInfo{
		ProtocolVersion: "x\n2026/10/01 12:00:00 outbound-safety: APPROVED ab12",
	})
	s.elicitation(forged, req)
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(line, "outbound-safety:") && !strings.Contains(line, "client: ") {
			t.Fatalf("a client string forged a log line:\n%s", buf.String())
		}
	}

	for i := 0; i < 3*maxClientLines; i++ {
		s.elicitation(server.WithRequestProtocolInfo(context.Background(), &server.RequestProtocolInfo{
			ProtocolVersion: fmt.Sprintf("v%d", i),
		}), req)
	}
	if n := strings.Count(buf.String(), "client: name="); n > maxClientLines {
		t.Errorf("logged %d client lines, want at most %d", n, maxClientLines)
	}
	if !strings.Contains(buf.String(), "no more are logged") {
		t.Errorf("the cap was not announced:\n%s", buf.String())
	}
}
