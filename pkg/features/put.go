package features

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/exchange"
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
				"description": "The file's content as standard base64 (A-Z a-z 0-9 + /; padding optional; line breaks ignored), for binary files such as images. Not with content.",
			},
		},
		"required": []string{"name"},
	},
	Handler: putHandler,
}

func putHandler(_ context.Context, params map[string]interface{}) (*FeatureResult, error) {
	const nothing = "Nothing was written."
	fail := func(msg, guidance string) (*FeatureResult, error) {
		return &FeatureResult{Success: false, Message: msg, Guidance: guidance}, nil
	}

	// The name is checked before anything else, and refused rather than
	// repaired: a name that fails the rule is the shape of an attempt to
	// reach outside the directory (ADR-012).
	name, _ := params["name"].(string)
	if name == "" {
		return fail("name is required: a bare name such as chart.png", nothing)
	}
	if err := exchange.ValidateName(name); err != nil {
		return fail(err.Error(), "Pass a bare name such as chart.png. "+nothing)
	}

	rawContent, hasContent := params["content"]
	rawB64, hasB64 := params["base64"]
	hasContent = hasContent && rawContent != nil
	hasB64 = hasB64 && rawB64 != nil
	if hasContent == hasB64 {
		return fail("put takes exactly one of content= (text) or base64= (binary)", nothing)
	}

	var data []byte
	if hasContent {
		s, ok := rawContent.(string)
		if !ok {
			return fail("content must be a string", nothing)
		}
		data = []byte(s)
	} else {
		s, ok := rawB64.(string)
		if !ok {
			return fail("base64 must be a string", nothing)
		}
		var err error
		if data, err = decodePutBase64(s); err != nil {
			return fail(err.Error(), nothing)
		}
	}
	if len(data) == 0 {
		return fail("The content is empty; put does not write empty files", nothing)
	}
	if len(data) > maxPutBytes {
		return fail(fmt.Sprintf("The content is %d bytes, over put's %d byte (5 MiB) limit", len(data), maxPutBytes), nothing)
	}

	dir, err := exchange.Open()
	if err != nil {
		return fail(err.Error(), nothing)
	}
	defer dir.Close()

	created, err := dir.Create(name)
	if err != nil {
		// Create's collision message names download's parameter.
		return fail(strings.Replace(err.Error(), "pass filename=", "pass name=", 1), nothing)
	}
	_, err = created.File.Write(data)
	if closeErr := created.File.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("finalizing file: %w", closeErr)
	}
	if err != nil {
		if rmErr := dir.Remove(created.Name); rmErr != nil {
			log.Printf("put: partial %s not removed: %v", created.Name, rmErr)
			return fail(fmt.Sprintf("Writing %s failed (%v), and removing the partial file failed too, so it is still in the exchange directory.", created.Name, err),
				"Do not attach it; tell the operator.")
		}
		return fail(fmt.Sprintf("Writing %s failed: %v", created.Name, err), "The partial file was removed. "+nothing)
	}

	msg := fmt.Sprintf("Wrote %d bytes into the exchange directory as %s", len(data), created.Name)
	if notice := created.Notice(); notice != "" {
		msg += ". " + notice
	}
	info := map[string]interface{}{
		"name": created.Name,
		"size": len(data),
		"path": dir.DisplayPath(created.Name),
	}
	if created.Renamed() {
		info["requested"] = created.Requested
	}
	return &FeatureResult{
		Success:     true,
		Message:     msg,
		Data:        info,
		NextActions: []string{fmt.Sprintf("say to='<destination>' files=['%s']", created.Name)},
	}, nil
}

// decodePutBase64 decodes the standard alphabet, padded or not. Line breaks
// are ignored (the decoder skips \r and \n), so wrapped output from a base64
// tool decodes as is. The URL-safe alphabet is refused by name rather than
// guessed at: one alphabet keeps a malformed payload from decoding into
// something other than what was meant.
func decodePutBase64(s string) ([]byte, error) {
	if strings.ContainsAny(s, "-_") {
		return nil, fmt.Errorf("base64 must use the standard alphabet (+ and /); this looks like URL-safe base64 (- and _)")
	}
	enc := base64.RawStdEncoding
	if strings.Contains(s, "=") {
		enc = base64.StdEncoding
	}
	data, err := enc.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64 is not valid standard base64: %v", err)
	}
	return data, nil
}
