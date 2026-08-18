package version_test

// The release manifests are otherwise only exercised at release time — by CI, on
// a tag, after npm has already published. A version out of step or a field
// missing fails there, which is the worst moment to find out. Everything here is
// offline and compares the files against each other.
//
// This is the check that would have caught the live drift these tests were
// written after: mcpb/manifest.json and the npm packages sat at 1.2.0 through
// releases that shipped as 1.4.0.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../.."

type serverJSON struct {
	Schema      string `json:"$schema"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
	WebsiteURL  string `json:"websiteUrl"`
	Repository  struct {
		URL    string `json:"url"`
		Source string `json:"source"`
		ID     string `json:"id"`
	} `json:"repository"`
	Packages []struct {
		RegistryType string `json:"registryType"`
		Identifier   string `json:"identifier"`
		Version      string `json:"version"`
		Transport    struct {
			Type string `json:"type"`
		} `json:"transport"`
	} `json:"packages"`
}

type packageJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	License              string            `json:"license"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

func readJSON[T any](t *testing.T, rel string) T {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return out
}

var platforms = []string{
	"darwin-amd64", "darwin-arm64",
	"linux-amd64", "linux-arm64",
	"windows-amd64", "windows-arm64",
}

// server.json records the version twice — once for the server entry and once for
// the npm package it points at. A mismatch publishes a registry entry naming a
// tarball that does not exist, and nothing on the npm side would catch it.
func TestServerJSONRecordsOneVersion(t *testing.T) {
	server := readJSON[serverJSON](t, "server.json")

	if len(server.Packages) == 0 {
		t.Fatal("server.json advertises no packages")
	}
	for _, p := range server.Packages {
		if p.Version != server.Version {
			t.Errorf("packages[%s].version is %q, server version is %q", p.Identifier, p.Version, server.Version)
		}
	}
}

func TestEveryManifestAgreesOnTheVersion(t *testing.T) {
	server := readJSON[serverJSON](t, "server.json")
	wrapper := readJSON[packageJSON](t, "npm/slack-mcp-server/package.json")
	mcpb := readJSON[struct {
		Version string `json:"version"`
	}](t, "mcpb/manifest.json")

	want := wrapper.Version

	if server.Version != want {
		t.Errorf("server.json version is %q, npm wrapper is %q", server.Version, want)
	}
	if mcpb.Version != want {
		t.Errorf("mcpb/manifest.json version is %q, npm wrapper is %q", mcpb.Version, want)
	}

	for _, plat := range platforms {
		p := readJSON[packageJSON](t, filepath.Join("npm", "slack-mcp-server-"+plat, "package.json"))
		if p.Version != want {
			t.Errorf("npm/%s version is %q, npm wrapper is %q", plat, p.Version, want)
		}
	}

	// The wrapper resolves a platform binary through optionalDependencies. If
	// those pin an older version, installing the wrapper pulls a stale binary.
	for dep, ver := range wrapper.OptionalDependencies {
		if ver != want {
			t.Errorf("optionalDependencies[%s] is %q, want %q", dep, ver, want)
		}
	}
}

// The registry grants io.github.<owner>/* from the OIDC repository_owner claim.
// A name outside that namespace cannot be published by this repository at all.
func TestServerJSONNameIsClaimable(t *testing.T) {
	server := readJSON[serverJSON](t, "server.json")

	if !strings.HasPrefix(server.Name, "io.github.aaronsb/") {
		t.Errorf("server.json name %q is outside the namespace this repo's OIDC token can claim", server.Name)
	}
}

func TestServerJSONPointsAtThePublishedPackage(t *testing.T) {
	server := readJSON[serverJSON](t, "server.json")
	wrapper := readJSON[packageJSON](t, "npm/slack-mcp-server/package.json")

	var npm *struct {
		RegistryType string `json:"registryType"`
		Identifier   string `json:"identifier"`
		Version      string `json:"version"`
		Transport    struct {
			Type string `json:"type"`
		} `json:"transport"`
	}
	for i := range server.Packages {
		if server.Packages[i].RegistryType == "npm" {
			npm = &server.Packages[i]
			break
		}
	}
	if npm == nil {
		t.Fatal("server.json advertises no npm package")
	}

	if npm.Identifier != wrapper.Name {
		t.Errorf("server.json advertises %q, this repo publishes %q", npm.Identifier, wrapper.Name)
	}
	if npm.Transport.Type != "stdio" {
		t.Errorf("npm package transport is %q, want stdio", npm.Transport.Type)
	}
}

// Without repository metadata the registry lists a server with no way to inspect
// what it runs. The forge id is GitHub's numeric identifier rather than the
// path: it survives a rename, and it changes if a repository is deleted and
// recreated — which is how a registry detects someone recreating an abandoned
// repo at the same path to inherit its reputation.
func TestServerJSONSaysWhereTheSourceIs(t *testing.T) {
	server := readJSON[serverJSON](t, "server.json")

	if server.Repository.URL != "https://github.com/aaronsb/slack-mcp" {
		t.Errorf("repository.url is %q", server.Repository.URL)
	}
	if server.Repository.Source != "github" {
		t.Errorf("repository.source is %q, want github", server.Repository.Source)
	}
	if server.Repository.ID == "" {
		t.Error("repository.id is empty — the path alone does not survive a delete-and-recreate")
	}
	if server.Title == "" {
		t.Error("server.json has no title; the listing falls back to showing the description as a name")
	}
	if server.WebsiteURL == "" {
		t.Error("server.json has no websiteUrl")
	}
}

// The six platform packages are public on npmjs.com. Left unattended they
// render with no README, no homepage and no bug link, which is what an
// abandoned package looks like. `make npm-metadata` writes these; this asserts
// nobody edited one copy by hand and left the other five behind.
func TestPlatformPackagesCarryPublishableMetadata(t *testing.T) {
	wrapper := readJSON[packageJSON](t, "npm/slack-mcp-server/package.json")

	for _, plat := range platforms {
		dir := filepath.Join("npm", "slack-mcp-server-"+plat)

		var pkg struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			License     string   `json:"license"`
			Files       []string `json:"files"`
			Homepage    string   `json:"homepage"`
			OS          []string `json:"os"`
			CPU         []string `json:"cpu"`
			Author      struct {
				Name string `json:"name"`
			} `json:"author"`
			Bugs struct {
				URL string `json:"url"`
			} `json:"bugs"`
			Repository struct {
				URL string `json:"url"`
			} `json:"repository"`
		}
		raw, err := os.ReadFile(filepath.Join(repoRoot, dir, "package.json"))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		if err := json.Unmarshal(raw, &pkg); err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}

		if pkg.Description == "" {
			t.Errorf("%s has no description", plat)
		}
		if pkg.Homepage == "" || pkg.Bugs.URL == "" || pkg.Author.Name == "" {
			t.Errorf("%s is missing homepage, bugs, or author — it will look abandoned on npm", plat)
		}
		if pkg.License != wrapper.License && pkg.License == "" {
			t.Errorf("%s has no license", plat)
		}
		if pkg.Repository.URL == "" {
			t.Errorf("%s does not say where its source is", plat)
		}

		// os/cpu are what make npm install exactly one of these. Without them
		// every user would download all six.
		if len(pkg.OS) == 0 || len(pkg.CPU) == 0 {
			t.Errorf("%s does not restrict os/cpu; npm would install it everywhere", plat)
		}
		if len(pkg.Files) == 0 {
			t.Errorf("%s has no files allowlist", plat)
		}

		// A README is what npmjs.com shows on the package page.
		if _, err := os.Stat(filepath.Join(repoRoot, dir, "README.md")); err != nil {
			t.Errorf("%s has no README: %v", plat, err)
		}
	}
}
