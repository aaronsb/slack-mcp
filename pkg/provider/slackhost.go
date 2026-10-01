package provider

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The session credentials (the xoxc bearer token and the d cookie) go only
// to Slack hosts. These checks decide what a Slack host is, for downloads,
// upload addresses, and every redirect hop.

// IsSlackHost reports whether host is slack.com or a subdomain of it. The
// host must be plain ASCII letters, digits, dots and hyphens: anything else
// is refused before any case folding, because Unicode folding maps
// look-alikes onto ASCII (U+212A KELVIN SIGN folds to 'k') while the request
// would go out as punycode to a different domain.
func IsSlackHost(host string) bool {
	if host == "" {
		return false
	}
	lower := make([]byte, len(host))
	for i := 0; i < len(host); i++ {
		c := host[i]
		switch {
		case c >= 'A' && c <= 'Z':
			lower[i] = c + ('a' - 'A')
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-':
			lower[i] = c
		default:
			return false
		}
	}
	h := string(lower)
	return h == "slack.com" || (len(h) > len(".slack.com") && strings.HasSuffix(h, ".slack.com"))
}

// CheckSlackURL refuses a URL the credentials must not be sent to: it must
// be https, carry no userinfo, use the default port, and name a Slack host.
func CheckSlackURL(u *url.URL) error {
	if u == nil {
		return errors.New("no URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("URL must be https, got %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("URL carries userinfo")
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("URL names port %q; only the https default is allowed", p)
	}
	if !IsSlackHost(u.Hostname()) {
		return fmt.Errorf("%q is not a Slack host", u.Hostname())
	}
	return nil
}

// isBaseURLHost reports whether u is on the WithBaseURL host: the same
// scheme and host:port, compared byte for byte, and no userinfo.
func isBaseURLHost(baseURL string, u *url.URL) bool {
	if baseURL == "" || u == nil || u.User != nil {
		return false
	}
	b, err := url.Parse(baseURL)
	if err != nil || b.Host == "" {
		return false
	}
	return u.Scheme == b.Scheme && u.Host == b.Host
}

// redirectPolicy is the CheckRedirect for the client that carries the
// session credentials: every hop must pass CheckSlackURL or be on the
// WithBaseURL host.
func redirectPolicy(baseURL string) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if isBaseURLHost(baseURL, req.URL) {
			return nil
		}
		if err := CheckSlackURL(req.URL); err != nil {
			return fmt.Errorf("refusing redirect: %w", err)
		}
		return nil
	}
}
