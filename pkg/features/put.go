package features

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/exchange"
	"github.com/aaronsb/slack-mcp/pkg/lifecycle"
)

// maxPutBytes caps what one put writes, counted after base64 decoding.
const maxPutBytes = 5 << 20

// Put writes a new file into the exchange directory for say files= to
// attach (ADR-012, amendment 2026-10-01). It is write-only: it never
// overwrites, reads back, lists, or deletes, and it makes no Slack call.
var Put = &Feature{
	Name:        "put",
	Description: "Write a new file into the exchange directory for say files= to attach, for clients whose own file tools cannot reach the server's filesystem (a sandbox on another machine, a remote server). name is a bare name, not a path; pass the content as text in content= or as standard base64 in base64=, exactly one. Never overwrites: a taken name is saved with a ' (n)' suffix and the result says so. At most 5 MiB.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Name to save as in the exchange directory: a bare name such as chart.png, not a path",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "The file's content as UTF-8 text, written as given. Not with base64.",
			},
			"base64": map[string]interface{}{
				"type":        "string",
				"description": "The file's content as standard base64 (A-Z a-z 0-9 + /; padding optional; may be wrapped into equal-length lines), for binary files such as images. Not with content.",
			},
		},
		"required": []string{"name"},
	},
	Handler: putHandler,
}

// putWrite writes the staged file; tests replace it to fail a write.
var putWrite = func(w io.Writer, b []byte) (int, error) { return w.Write(b) }

func putHandler(_ context.Context, params map[string]interface{}) (*FeatureResult, error) {
	const nothing = "Nothing was written."
	fail := func(msg, guidance string) (*FeatureResult, error) {
		return &FeatureResult{Success: false, Message: msg, Guidance: guidance}, nil
	}

	// The name is checked before anything else, and refused rather than
	// repaired: a name that fails the rule is the shape of an attempt to
	// reach outside the directory (ADR-012).
	rawName, hasName := params["name"]
	name, isString := rawName.(string)
	switch {
	case hasName && rawName != nil && !isString:
		return fail("name must be a string: a bare name such as chart.png", nothing)
	case name == "":
		return fail("name is required: a bare name such as chart.png", nothing)
	}
	if err := exchange.ValidateName(name); err != nil {
		return fail(err.Error(), "Pass a bare name such as chart.png. "+nothing)
	}

	content, hasContent, err := putParam(params, "content")
	if err != nil {
		return fail(err.Error(), nothing)
	}
	b64, hasB64, err := putParam(params, "base64")
	if err != nil {
		return fail(err.Error(), nothing)
	}
	// An empty value beside a non-empty one is taken as absent; two empty
	// values are an empty file.
	if hasContent && hasB64 {
		switch {
		case content == "":
			hasContent = false
		case b64 == "":
			hasB64 = false
		}
	}
	if hasContent == hasB64 {
		return fail("put takes exactly one of content= (text) or base64= (binary)", nothing)
	}

	overCap := func(n int) (*FeatureResult, error) {
		return fail(fmt.Sprintf("The content is %d bytes, over put's %d byte (5 MiB) limit", n, maxPutBytes), nothing)
	}
	var data []byte
	if hasContent {
		if len(content) > maxPutBytes {
			return overCap(len(content))
		}
		data = []byte(content)
	} else {
		// Bounded before decoding, from the characters that carry data.
		if n := len(b64) - strings.Count(b64, "\n") - strings.Count(b64, "\r"); n > base64.StdEncoding.EncodedLen(maxPutBytes) {
			return overCap(base64.StdEncoding.DecodedLen(n))
		}
		if data, err = decodePutBase64(b64); err != nil {
			return fail(err.Error(), nothing)
		}
		if len(data) > maxPutBytes {
			return overCap(len(data))
		}
	}
	if len(data) == 0 {
		return fail("The content is empty; put does not write empty files", nothing)
	}

	dir, err := exchange.Open()
	if err != nil {
		return fail(err.Error(), nothing)
	}
	defer dir.Close()

	// Written in full out of reach of every file parameter, then given its
	// name, so a say running meanwhile never reads a partial file.
	staged, err := dir.Stage()
	if err != nil {
		return fail(err.Error(), nothing)
	}
	discard := func(cause string) (*FeatureResult, error) {
		if rmErr := staged.Discard(); rmErr != nil {
			log.Printf("put: staged copy not removed: %v", rmErr)
			return fail(cause+". The staged copy could not be removed; no file parameter can name it, and the operator can delete it from the exchange directory's staging folder.", nothing)
		}
		return fail(cause, nothing)
	}
	_, err = putWrite(staged.File, data)
	if closeErr := staged.File.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("finalizing file: %w", closeErr)
	}
	if err != nil {
		return discard(fmt.Sprintf("Writing %s failed: %v", name, err))
	}
	created, err := staged.Commit(name)
	if err != nil {
		var collision *exchange.CollisionError
		if errors.As(err, &collision) {
			return discard(collision.Message("name"))
		}
		return discard(err.Error())
	}

	path := dir.DisplayPath(created.Name)
	msg := fmt.Sprintf("Wrote %d bytes into the exchange directory as %s", len(data), created.Name)
	if notice := created.Notice(); notice != "" {
		msg += ". " + notice
	}
	info := map[string]interface{}{
		"name": created.Name,
		"size": len(data),
		"path": path,
	}
	if created.Renamed() {
		info["requested"] = created.Requested
	}
	next := []string{fmt.Sprintf("say to='<destination>' files=['%s']", created.Name)}
	if dep, _ := lifecycle.DeploymentFromEnv(); dep == lifecycle.Remote {
		info["host"] = "server"
		next = append(next, "The path is on the server's host, not this client's.")
	}
	return &FeatureResult{
		Success:     true,
		Message:     msg,
		Data:        info,
		NextActions: next,
	}, nil
}

// putParam reads an optional string parameter: absent and null are the
// same, anything else must be a string.
func putParam(params map[string]interface{}, key string) (value string, present bool, err error) {
	raw, ok := params[key]
	if !ok || raw == nil {
		return "", false, nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", false, fmt.Errorf("%s must be a string", key)
	}
	return s, true, nil
}

// decodePutBase64 decodes the standard alphabet strictly (nonzero padding
// bits are refused), padded or not. Line breaks, LF or CRLF, are accepted
// only where wrapping puts them: between lines of one length, the last no
// longer, with at most one break at the end. The URL-safe alphabet is
// refused by name rather than guessed at.
func decodePutBase64(s string) ([]byte, error) {
	if strings.ContainsAny(s, "-_") {
		return nil, errors.New("base64 must use the standard alphabet (+ and /); this looks like URL-safe base64 (- and _)")
	}
	joined, err := unwrapBase64(s)
	if err != nil {
		return nil, err
	}
	enc := base64.RawStdEncoding.Strict()
	if strings.HasSuffix(joined, "=") {
		enc = base64.StdEncoding.Strict()
	}
	data, err := enc.DecodeString(joined)
	if err != nil {
		return nil, fmt.Errorf("base64 is not valid standard base64: %v", err)
	}
	return data, nil
}

// unwrapBase64 removes line wrapping, refusing breaks anywhere else. The
// decoder would skip CR and LF wherever they fall; checking them here keeps
// a stray break from passing as wrapping.
func unwrapBase64(s string) (string, error) {
	if !strings.ContainsAny(s, "\r\n") {
		return s, nil
	}
	refuse := errors.New("base64 has line breaks outside line wrapping: wrap it into lines of one length (the last may be shorter), or send it on one line")
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	width := 0
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.Contains(line, "\r") {
			return "", refuse
		}
		switch {
		case i == 0:
			width = len(line)
		case i < len(lines)-1 && len(line) != width, len(line) > width:
			return "", refuse
		}
		lines[i] = line
	}
	return strings.Join(lines, ""), nil
}
