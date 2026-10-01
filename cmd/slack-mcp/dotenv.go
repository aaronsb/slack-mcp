package main

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/joho/godotenv"
)

// dotEnvAllowed lists the only keys a .env file may set. Every other
// setting comes from the environment the MCP client provides (ADR-014).
// A new setting stays off this list unless it has no security effect.
var dotEnvAllowed = map[string]bool{
	"SLACK_MCP_PERSONALITY": true,
	"SLACK_MCP_NO_BROWSER":  true,
}

// loadDotEnv applies the .env file at path under the allowlist. A key the
// environment already sets is left alone, as before. An allowlisted key is
// set when absent. Any other key absent from the environment is an error
// naming it, and nothing is set. A missing file is not an error; a file
// that does not parse is.
func loadDotEnv(path string, lookup func(string) (string, bool), setenv func(string, string) error) error {
	vars, err := godotenv.Read(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%s: %w", path, err)
	}

	var refused []string
	for key := range vars {
		if _, set := lookup(key); set || dotEnvAllowed[key] {
			continue
		}
		refused = append(refused, key)
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return fmt.Errorf("%s sets %s; .env may only set: SLACK_MCP_PERSONALITY, SLACK_MCP_NO_BROWSER; set other settings in your MCP client config",
			path, strings.Join(refused, ", "))
	}

	for key, value := range vars {
		if _, set := lookup(key); set {
			continue
		}
		if err := setenv(key, value); err != nil {
			return fmt.Errorf("%s: set %s: %w", path, key, err)
		}
	}
	return nil
}
