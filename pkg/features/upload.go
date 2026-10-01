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
//  1. parameter checks (sayHandler) and every file named, opened, checked
//     on its handle, and read once into memory (readUploadFiles);
//  2. preSendUpload, the one boundary between local checks and Slack;
//  3. the destination resolved under the write policy;
//  4. per file, files.getUploadURLExternal, a host check on the URL it
//     returns, and a multipart POST of the bytes read in step 1;
//  5. one files.completeUploadExternal carrying every file, so N files
//     arrive as one message;
//  6. a bounded files.info poll for the message ts.
//
// Steps 1 and 2 make no Slack call, so any refusal there sends nothing,
// the text included.

// maxUploadFiles matches Slack's composer: one message carries at most 10
// files.
const maxUploadFiles = 10

// maxUploadBytes caps one file. A variable so tests can lower it.
var maxUploadBytes int64 = provider.MaxUploadBytes

// shareTSDelays are the waits before each files.info poll for the message
// ts, which Slack fills in asynchronously: three tries, 1.5s in all. A
// variable so tests need not wait.
var shareTSDelays = []time.Duration{300 * time.Millisecond, 500 * time.Millisecond, 700 * time.Millisecond}

// uploadFile is one file as read from the exchange directory. Data is the
// only copy of the bytes: what any pre-send check reads is what is sent.
type uploadFile struct {
	Name string
	Type string
	Data []byte
}

// outboundUpload is everything a say files= call will send, assembled
// before any Slack call.
type outboundUpload struct {
	// To is the destination as the caller named it, unresolved: nothing
	// has looked it up or opened a DM for it yet.
	To string
	// Thread is the thread ts, or empty for a top-level message.
	Thread string
	// Text is the comment as the caller supplied it; Comment is the
	// initial_comment as it will be sent (NormalizeMrkdwn). Both are empty
	// for files alone.
	Text    string
	Comment string
	// Files are the files in the order named, bytes included.
	Files []uploadFile
}

// preSendUpload runs after every file is validated and read and before the
// first Slack call. A non-nil result refuses the call and is returned
// as-is; nothing has been sent. The outbound-safety layer (ADR-013: strike
// lock, quarantine, scanner, approval gate) is inserted here. A variable so
// tests can stand in for it.
var preSendUpload = func(ctx context.Context, ap *provider.ApiProvider, up *outboundUpload) *FeatureResult {
	return nil
}

// parseFilesParam reads say's files= parameter: a non-empty array of bare
// names, at most maxUploadFiles. The error is the caller-facing refusal.
func parseFilesParam(v interface{}) ([]string, error) {
	var names []string
	switch list := v.(type) {
	case []string:
		names = append(names, list...)
	case []interface{}:
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("files must be an array of bare file names, such as files=['report.pdf']")
			}
			names = append(names, s)
		}
	default:
		return nil, errors.New("files must be an array of bare file names, such as files=['report.pdf']")
	}
	if len(names) == 0 {
		return nil, errors.New("files is empty: name at least one file in the exchange directory, or drop files= to post text alone")
	}
	if len(names) > maxUploadFiles {
		return nil, fmt.Errorf("files names %d files; one message carries at most %d. Split them across messages", len(names), maxUploadFiles)
	}
	return names, nil
}

// readUploadFiles opens and reads every named file from the exchange
// directory. It checks them all before failing, so one answer names every
// problem; a missing name carries the exchange package's matching-names
// hint. No Slack call is made.
func readUploadFiles(names []string) ([]uploadFile, []string, error) {
	dir, err := exchange.Open()
	if err != nil {
		return nil, nil, err
	}
	defer dir.Close()

	files := make([]uploadFile, 0, len(names))
	var problems []string
	for _, name := range names {
		data, err := dir.ReadFile(name, maxUploadBytes)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if len(data) == 0 {
			problems = append(problems, fmt.Sprintf("%s is empty; Slack does not accept an empty file", name))
			continue
		}
		files = append(files, uploadFile{Name: name, Type: fileType(name, data), Data: data})
	}
	return files, problems, nil
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
		if len(names) == 1 {
			head = "The file could not be attached:"
		}
		return fail(head+"\n\n"+strings.Join(problems, "\n\n"),
			nothingSent+" files= takes bare names inside the exchange directory ("+exchange.Locate().Path+"); copy a file in with your own file tools, then retry.")
	}

	up := &outboundUpload{To: to, Thread: thread, Files: files}
	if strings.TrimSpace(supplied) != "" {
		up.Text = supplied
		up.Comment = text.NormalizeMrkdwn(supplied)
	}
	if refusal := preSendUpload(ctx, apiProvider, up); refusal != nil {
		return refusal, nil
	}

	api, err := apiProvider.Provide()
	if err != nil {
		return fail(fmt.Sprintf("Failed to connect to Slack: %v", err), nothingSent)
	}
	channelID, terr := resolveTarget(ctx, apiProvider, to, provider.WritePolicy, "to")
	if terr != nil {
		return terr.result(), nil
	}

	ids, err := shareFiles(ctx, apiProvider, api, channelID, up)
	if err != nil {
		return fail(err.Error(), "")
	}

	ts := shareTS(ctx, api, ids[0], channelID)
	return sayFilesResult(destinationName(apiProvider, channelID, to), up, ids, ts), nil
}

// shareFiles uploads every file and completes them together, so they
// arrive as one message. It returns the Slack file IDs in order. On error
// nothing was shared: files uploaded before the failure stay private and
// unshared, and the error says so.
func shareFiles(ctx context.Context, ap *provider.ApiProvider, api *slack.Client, channelID string, up *outboundUpload) ([]string, error) {
	summaries := make([]slack.FileSummary, 0, len(up.Files))
	unshared := func() string {
		if len(summaries) == 0 {
			return "Nothing was shared."
		}
		return fmt.Sprintf("Nothing was shared; %d file(s) uploaded before the failure stay private and unshared.", len(summaries))
	}

	for _, f := range up.Files {
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
		InitialComment:  up.Comment,
		ThreadTimestamp: up.Thread,
	}); err != nil {
		return nil, fmt.Errorf("Uploaded %d file(s), but Slack refused to share them: %v. Nothing was posted; the uploads stay private and unshared.", len(summaries), err)
	}

	ids := make([]string, len(summaries))
	for i, s := range summaries {
		ids[i] = s.ID
	}
	return ids, nil
}

// shareTS polls files.info for the ts of the message that shared fileID in
// channelID. completeUploadExternal does not return it, and Slack records
// the share asynchronously. Empty means Slack had not reported it within
// the bounded wait; the caller says so rather than guessing.
func shareTS(ctx context.Context, api *slack.Client, fileID, channelID string) string {
	for _, d := range shareTSDelays {
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(d):
		}
		file, _, _, err := api.GetFileInfoContext(ctx, fileID, 0, 0)
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

// destinationName names a conversation for output without exposing its ID:
// a channel by its cached name, anything else as the caller wrote it.
func destinationName(ap *provider.ApiProvider, channelID, to string) string {
	if ch, ok := ap.LookupChannel(channelID); ok && !ch.IsIM && !ch.IsMpIM && ch.Name != "" {
		return "#" + ch.Name
	}
	return to
}

func sayFilesResult(dest string, up *outboundUpload, ids []string, ts string) *FeatureResult {
	listed := make([]map[string]interface{}, len(up.Files))
	for i, f := range up.Files {
		listed[i] = map[string]interface{}{
			"name":   f.Name,
			"size":   len(f.Data),
			"type":   f.Type,
			"fileId": ids[i],
		}
	}
	data := map[string]interface{}{
		"destination": dest,
		"files":       listed,
		"thread":      up.Thread,
		"comment":     up.Comment != "",
		"ts":          ts,
	}

	var next []string
	switch {
	case up.Thread != "":
		next = []string{
			fmt.Sprintf("Read the thread: messages target='%s' around='%s'", up.To, up.Thread),
			fmt.Sprintf("Continue: say to='%s' thread='%s'", up.To, up.Thread),
		}
	case ts != "":
		next = []string{
			fmt.Sprintf("Reply in its thread: say to='%s' thread='%s'", up.To, ts),
			fmt.Sprintf("Watch for responses: messages target='%s' since='30m'", up.To),
		}
	default:
		next = []string{fmt.Sprintf("Watch for responses: messages target='%s' since='30m'", up.To)}
	}

	return &FeatureResult{
		Success:     true,
		Message:     fmt.Sprintf("Shared %d file(s) to %s", len(up.Files), dest),
		Data:        data,
		NextActions: next,
	}
}
