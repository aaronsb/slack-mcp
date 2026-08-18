package version_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// server.json is validated against the registry's own schema rather than
// against rules written out by hand here.
//
// v1.5.0 shipped to npm and then bounced off the registry with a 422: the
// description was 119 characters against a limit of 100. That limit was in the
// schema the whole time. Deriving the constraints from the schema catches the
// rules nobody has been bitten by yet, which hand-written assertions cannot.
//
// The schema is vendored (scripts/update-registry-schema.sh) so this runs
// offline and a schema change arrives as a reviewable diff rather than as a
// build that starts failing on its own.

type jsonSchema struct {
	Ref         string                `json:"$ref"`
	Definitions map[string]definition `json:"definitions"`
}

type definition struct {
	Type       string                `json:"type"`
	Required   []string              `json:"required"`
	Properties map[string]constraint `json:"properties"`
}

type constraint struct {
	Type      string   `json:"type"`
	MaxLength *int     `json:"maxLength"`
	MinLength *int     `json:"minLength"`
	Pattern   string   `json:"pattern"`
	Enum      []string `json:"enum"`
	Ref       string   `json:"$ref"`
	Items     *struct {
		Ref string `json:"$ref"`
	} `json:"items"`
}

func TestServerJSONSatisfiesTheRegistrySchema(t *testing.T) {
	var schema jsonSchema
	raw, err := os.ReadFile(filepath.Join(repoRoot, "docs/schemas/mcp-server.schema.json"))
	if err != nil {
		t.Fatalf("read vendored schema: %v (run scripts/update-registry-schema.sh)", err)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	var doc map[string]any
	rawDoc, err := os.ReadFile(filepath.Join(repoRoot, "server.json"))
	if err != nil {
		t.Fatalf("read server.json: %v", err)
	}
	if err := json.Unmarshal(rawDoc, &doc); err != nil {
		t.Fatalf("parse server.json: %v", err)
	}

	root := defName(schema.Ref)
	if root == "" {
		t.Fatalf("schema has no root $ref")
	}
	check(t, schema, root, doc, "")
}

func check(t *testing.T, schema jsonSchema, defKey string, doc map[string]any, path string) {
	t.Helper()

	def, ok := schema.Definitions[defKey]
	if !ok {
		return
	}

	for _, req := range def.Required {
		if _, present := doc[req]; !present {
			t.Errorf("%s%s is required by the registry schema and is missing", path, req)
		}
	}

	for field, c := range def.Properties {
		value, present := doc[field]
		if !present {
			continue
		}
		where := path + field

		if s, isString := value.(string); isString {
			// Characters, not bytes: descriptions here contain em dashes.
			if c.MaxLength != nil && len([]rune(s)) > *c.MaxLength {
				t.Errorf("%s is %d characters, schema allows %d:\n  %q", where, len([]rune(s)), *c.MaxLength, s)
			}
			if c.MinLength != nil && len([]rune(s)) < *c.MinLength {
				t.Errorf("%s is %d characters, schema requires at least %d", where, len([]rune(s)), *c.MinLength)
			}
			if c.Pattern != "" {
				re, err := regexp.Compile(c.Pattern)
				if err == nil && !re.MatchString(s) {
					t.Errorf("%s = %q does not match the schema pattern %s", where, s, c.Pattern)
				}
			}
		}

		// Recurse into nested objects and arrays of objects.
		if nested, isObj := value.(map[string]any); isObj && c.Ref != "" {
			check(t, schema, defName(c.Ref), nested, where+".")
		}
		if list, isList := value.([]any); isList && c.Items != nil && c.Items.Ref != "" {
			for i, item := range list {
				if nested, isObj := item.(map[string]any); isObj {
					check(t, schema, defName(c.Items.Ref), nested, fmt.Sprintf("%s[%d].", where, i))
				}
			}
		}
	}
}

func defName(ref string) string {
	const prefix = "#/definitions/"
	if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
		return ref[len(prefix):]
	}
	return ""
}
