package transport

import "net/http"

type UserAgentTransport struct {
	roundTripper http.RoundTripper
	userAgent    string
	cookie       string
}

func New(roundTripper http.RoundTripper, userAgent string, cookie string) *UserAgentTransport {
	return &UserAgentTransport{
		roundTripper: roundTripper,
		userAgent:    userAgent,
		cookie:       cookie,
	}
}

// RoundTrip implements the RoundTripper interface.
func (t *UserAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clonedReq := req.Clone(req.Context())
	clonedReq.Header.Set("User-Agent", t.userAgent)
	clonedReq.Header.Set("Cookie", "d="+t.cookie+";d-s=1744415074")

	return t.roundTripper.RoundTrip(clonedReq)
}
