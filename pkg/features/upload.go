package features

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/exchange"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/text"
	"github.com/slack-go/slack"
)

// say files= (#92): attach files from the exchange directory (ADR-012) to
// one message. The order is fixed:
//
//  1. preWriteLocal and the parameter checks (sayHandler);
//  2. every file named, opened, and checked on its handle, then read once
//     into memory (readUploadFiles);
//  3. the destination located without any Slack call
//     (resolveWriteDestination);
//  4. preSend, with the destination and every byte that will be sent;
//  5. openDestination, which opens a DM only now;
//  6. per file, files.getUploadURLExternal, a host check on the URL it
//     returns, and a multipart POST of the bytes read in step 2;
//  7. one files.completeUploadExternal carrying every file, so N files
//     arrive as one message;
//  8. a bounded files.info poll for the message ts.
//
// Steps 1 to 4 make no Slack call, so any refusal there sends nothing, the
// text included.

// maxUploadFiles matches Slack's composer: one message carries at most 10
// files.
const maxUploadFiles = 10

// maxUploadBytes caps one file and maxUploadTotalBytes one call, since every
// file is held in memory until the upload ends. Variables so tests can
// lower them.
var (
	maxUploadBytes      int64 = provider.MaxUploadBytes
	maxUploadTotalBytes int64 = 1 << 30
)

// shareTSDelays are the waits before each files.info poll for the message
// ts, which Slack fills in asynchronously: three tries, 1.5s in all. A
// variable so tests need not wait. Each poll is bounded by shareTSTimeout.
var shareTSDelays = []time.Duration{300 * time.Millisecond, 500 * time.Millisecond, 700 * time.Millisecond}

const shareTSTimeout = 2 * time.Second

// parseFilesParam reads say's files= parameter: a non-empty array of
// distinct bare names, at most maxUploadFiles. The error is the
// caller-facing refusal.
func parseFilesParam(v interface{}) ([]string, error) {
	notNames := errors.New("files must be an array of bare file names, such as files=['report.pdf']")
	var names []string
	switch list := v.(type) {
	case []string:
		names = append(names, list...)
	case []interface{}:
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, notNames
			}
			names = append(names, s)
		}
	default:
		return nil, notNames
	}
	if len(names) == 0 {
		return nil, errors.New("files is empty: name at least one file in the exchange directory, or drop files= to post text alone")
	}
	if len(names) > maxUploadFiles {
		return nil, fmt.Errorf("files names %d files; one message carries at most %d. Split them across messages", len(names), maxUploadFiles)
	}
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			return nil, fmt.Errorf("files names %s more than once; name each file once", n)
		}
		seen[n] = true
	}
	return names, nil
}

// readUploadFiles reads every named file from the exchange directory in two
// passes. The first opens and checks each name, holding the handles, and
// collects every problem, so one answer names them all; a missing name
// carries the exchange package's matching-names hint. Only when every file
// passes, and their sizes together fit maxUploadTotalBytes, does the second
// pass read each held handle, bounded by its checked size. No Slack call.
func readUploadFiles(names []string) ([]uploadFile, []string, error) {
	dir, err := exchange.Open()
	if err != nil {
		return nil, nil, err
	}
	defer dir.Close()

	held := make([]*exchange.File, 0, len(names))
	defer func() {
		for _, f := range held {
			f.Close()
		}
	}()
	var problems []string
	var total int64
	for _, name := range names {
		f, err := dir.Open(name, maxUploadBytes)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		held = append(held, f)
		if f.Size == 0 {
			problems = append(problems, fmt.Sprintf("%s is empty; Slack does not accept an empty file", name))
		}
		total += f.Size
	}
	if len(problems) > 0 {
		return nil, problems, nil
	}
	if total > maxUploadTotalBytes {
		var b strings.Builder
		fmt.Fprintf(&b, "The files total %s (%d bytes), over the %s limit for one say:", humanSize(total), total, humanSize(maxUploadTotalBytes))
		for _, f := range held {
			fmt.Fprintf(&b, "\n- %s: %s (%d bytes)", f.Name, humanSize(f.Size), f.Size)
		}
		return nil, []string{b.String()}, nil
	}

	files := make([]uploadFile, 0, len(held))
	for _, f := range held {
		data, err := f.ReadStated()
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		files = append(files, uploadFile{Name: f.Name, Type: fileType(f.Name, data), Data: data})
	}
	if len(problems) > 0 {
		return nil, problems, nil
	}
	return files, nil, nil
}

// fileType is the media type shown for a file: by extension, else sniffed
// from its bytes, without parameters.
func fileType(name string, data []byte) string {
	t := mime.TypeByExtension(filepath.Ext(name))
	if t == "" {
		t = http.DetectContentType(data)
	}
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

func sayFilesHandler(ctx context.Context, params map[string]interface{}, names []string) (*FeatureResult, error) {
	fail := func(msg, guidance string) (*FeatureResult, error) {
		return &FeatureResult{Success: false, Message: msg, Guidance: guidance}, nil
	}
	nothingSent := "Nothing was sent, the text included."

	to, _ := params["to"].(string)
	thread, _ := params["thread"].(string)
	supplied, _ := params["text"].(string)

	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return fail("Internal error: provider not available", "")
	}

	files, problems, err := readUploadFiles(names)
	if err != nil {
		return fail(err.Error(), nothingSent)
	}
	if len(problems) > 0 {
		head := fmt.Sprintf("%d of %d files could not be attached:", len(problems), len(names))
		if len(names) == 1 || len(problems) == 1 && strings.HasPrefix(problems[0], "The files total") {
			head = "The files could not be attached:"
		}
		return fail(head+"\n\n"+strings.Join(problems, "\n\n"),
			nothingSent+" files= takes bare names inside the exchange directory ("+exchange.Locate().Path+"); copy a file in with your own file tools, then retry.")
	}

	api, err := apiProvider.Provide()
	if err != nil {
		return fail(fmt.Sprintf("Failed to connect to Slack: %v", err), nothingSent)
	}
	dest, terr := resolveWriteDestination(ctx, apiProvider, to, "to")
	if terr != nil {
		return terr.result(), nil
	}

	out := &outbound{Thread: thread, Files: files}
	if strings.TrimSpace(supplied) != "" {
		out.Text = supplied
		out.Fallback = text.NormalizeMrkdwn(supplied)
	}
	if refusal := preSend(ctx, apiProvider, dest, out); refusal != nil {
		return refusal, nil
	}

	channelID, terr := openDestination(ctx, apiProvider, dest)
	if terr != nil {
		return terr.result(), nil
	}

	ids, err := shareFiles(ctx, apiProvider, api, channelID, out, to)
	if err != nil {
		return fail(err.Error(), "")
	}

	ts := shareTS(ctx, api, ids[0], channelID)
	res := sayFilesResult(dest, out, ids, ts)
	res.Guidance = strings.Join(append([]string{fmt.Sprintf("The files were %s.", sentPhrase(apiProvider))}, out.Warnings...), "\n")
	return res, nil
}

// shareFiles uploads every file and completes them together, so they
// arrive as one message. It returns the Slack file IDs in order.
func shareFiles(ctx context.Context, ap *provider.ApiProvider, api *slack.Client, channelID string, out *outbound, to string) ([]string, error) {
	summaries := make([]slack.FileSummary, 0, len(out.Files))
	unshared := func() string {
		if len(summaries) == 0 {
			return "Nothing was shared."
		}
		return fmt.Sprintf("Nothing was shared; the %d file(s) uploaded before the failure were not shared.", len(summaries))
	}

	for _, f := range out.Files {
		u, err := api.GetUploadURLExternalContext(ctx, slack.GetUploadURLExternalParameters{
			FileName: f.Name,
			FileSize: len(f.Data),
		})
		if err != nil {
			return nil, fmt.Errorf("Slack refused an upload slot for %s: %v. %s", f.Name, err, unshared())
		}
		if err := ap.CheckUploadURL(u.UploadURL); err != nil {
			log.Printf("say files: refused upload URL for %s: %v", f.Name, err)
			return nil, fmt.Errorf("Slack returned an upload address for %s that is not a Slack host (%v); no bytes were sent to it. %s", f.Name, err, unshared())
		}
		if err := api.UploadToURL(ctx, slack.UploadToURLParameters{
			UploadURL: u.UploadURL,
			Reader:    bytes.NewReader(f.Data),
			Filename:  f.Name,
		}); err != nil {
			return nil, fmt.Errorf("Uploading %s failed: %v. %s", f.Name, err, unshared())
		}
		summaries = append(summaries, slack.FileSummary{ID: u.FileID, Title: f.Name})
	}

	// initial_comment, not blocks: slack-go sends blocks only without an
	// initial_comment, so a rich_text comment would carry no mrkdwn
	// fallback, and whether completeUploadExternal honours blocks from a
	// session token is unverified (#92 step 0).
	if _, err := api.CompleteUploadExternalContext(ctx, slack.CompleteUploadExternalParameters{
		Files:           summaries,
		Channel:         channelID,
		InitialComment:  out.Fallback,
		ThreadTimestamp: out.Thread,
	}); err != nil {
		var se slack.SlackErrorResponse
		if errors.As(err, &se) {
			return nil, fmt.Errorf("Uploaded %d file(s), but Slack refused to share them: %v. Nothing was posted; the uploads were not shared.", len(summaries), err)
		}
		return nil, fmt.Errorf("Uploaded %d file(s), but the request to share them failed (%v), so whether the message was posted is unknown. Check with messages target='%s' since='5m' before retrying.", len(summaries), err, to)
	}

	ids := make([]string, len(summaries))
	for i, s := range summaries {
		ids[i] = s.ID
	}
	return ids, nil
}

// shareTS polls files.info for the ts of the message that shared fileID in
// channelID. completeUploadExternal does not return it, and Slack records
// the share asynchronously, under shares.public for a channel and
// shares.private for a private channel or DM. Empty means Slack had not
// reported it within the bounded wait; the caller says so rather than
// guessing.
func shareTS(ctx context.Context, api *slack.Client, fileID, channelID string) string {
	for _, d := range shareTSDelays {
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(d):
		}
		pctx, cancel := context.WithTimeout(ctx, shareTSTimeout)
		file, _, _, err := api.GetFileInfoContext(pctx, fileID, 0, 0)
		cancel()
		if err != nil {
			log.Printf("say files: files.info for the message ts: %v", err)
			continue
		}
		for _, shares := range []map[string][]slack.ShareFileInfo{file.Shares.Public, file.Shares.Private} {
			if list := shares[channelID]; len(list) > 0 && list[0].Ts != "" {
				return list[0].Ts
			}
		}
	}
	return ""
}

func sayFilesResult(dest *resolvedDestination, out *outbound, ids []string, ts string) *FeatureResult {
	listed := make([]map[string]interface{}, len(out.Files))
	for i, f := range out.Files {
		listed[i] = map[string]interface{}{
			"name":   f.Name,
			"size":   len(f.Data),
			"type":   f.Type,
			"fileId": ids[i],
		}
	}
	data := map[string]interface{}{
		"destination": dest.Name,
		"files":       listed,
		"thread":      out.Thread,
		"comment":     out.Fallback != "",
		"ts":          ts,
	}

	to := dest.Typed
	var next []string
	switch {
	case out.Thread != "":
		next = []string{
			fmt.Sprintf("Read the thread: messages target='%s' around='%s'", to, out.Thread),
			fmt.Sprintf("Continue: say to='%s' thread='%s'", to, out.Thread),
		}
	case ts != "":
		next = []string{
			fmt.Sprintf("Reply in its thread: say to='%s' thread='%s'", to, ts),
			fmt.Sprintf("Watch for responses: messages target='%s' since='30m'", to),
		}
	default:
		next = []string{fmt.Sprintf("Watch for responses: messages target='%s' since='30m'", to)}
	}

	return &FeatureResult{
		Success:     true,
		Message:     fmt.Sprintf("Shared %d file(s) to %s", len(out.Files), dest.Name),
		Data:        data,
		NextActions: next,
	}
}
