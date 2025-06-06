package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// InternalClient provides access to Slack's internal/undocumented endpoints
// that the browser uses for features not available in the public API
type InternalClient struct {
	httpClient *http.Client
	xoxcToken  string
	xoxdToken  string
	baseURL    string
}

// NewInternalClient creates a client for internal Slack endpoints
func NewInternalClient(xoxcToken, xoxdToken string) *InternalClient {
	return &InternalClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		xoxcToken: xoxcToken,
		xoxdToken: xoxdToken,
		baseURL:   "https://slack.com",
	}
}

// ClientCountsResponse represents the response from /api/client.counts
type ClientCountsResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	// Channels with unread info
	Channels []struct {
		ID           string `json:"id"`
		LastRead     string `json:"last_read"`
		Latest       string `json:"latest"`
		Updated      string `json:"updated"`
		MentionCount int    `json:"mention_count"`
		HasUnreads   bool   `json:"has_unreads"`
	} `json:"channels"`

	// Multi-person DMs
	MPIMs []struct {
		ID           string `json:"id"`
		LastRead     string `json:"last_read"`
		Latest       string `json:"latest"`
		Updated      string `json:"updated"`
		MentionCount int    `json:"mention_count"`
		HasUnreads   bool   `json:"has_unreads"`
	} `json:"mpims"`

	// Direct messages
	IMs []struct {
		ID           string `json:"id"`
		LastRead     string `json:"last_read"`
		Latest       string `json:"latest"`
		Updated      string `json:"updated"`
		MentionCount int    `json:"mention_count"`
		HasUnreads   bool   `json:"has_unreads"`
	} `json:"ims"`

	// Thread counts
	Threads struct {
		HasUnreads   bool `json:"has_unreads"`
		MentionCount int  `json:"mention_count"`
		UnreadCount  int  `json:"unread_count"`
		VipCount     int  `json:"vip_count"`
	} `json:"threads"`

	// Channel badges summary
	ChannelBadges struct {
		Channels       int `json:"channels"`
		DMs            int `json:"dms"`
		AppDMs         int `json:"app_dms"`
		ThreadMentions int `json:"thread_mentions"`
		ThreadUnreads  int `json:"thread_unreads"`
	} `json:"channel_badges"`

	// Alerts
	Alerts struct {
		ListsUserMentioned int `json:"lists_user_mentioned"`
	} `json:"alerts"`

	// Saved items
	Saved struct {
		UncompletedCount        int `json:"uncompleted_count"`
		UncompletedOverdueCount int `json:"uncompleted_overdue_count"`
		ArchivedCount           int `json:"archived_count"`
		CompletedCount          int `json:"completed_count"`
		TotalCount              int `json:"total_count"`
	} `json:"saved"`

	CountsLastFetched int64 `json:"counts_last_fetched"`
}

// GetClientCounts fetches unread counts using the internal client.counts endpoint
func (c *InternalClient) GetClientCounts(ctx context.Context) (*ClientCountsResponse, error) {
	result := &ClientCountsResponse{}
	err := c.callInternalAPI(ctx, "/api/client.counts", nil, result)
	return result, err
}

// ClientBootResponse represents a subset of the /api/client.boot response
type ClientBootResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	Self struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"self"`

	Team struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Domain string `json:"domain"`
	} `json:"team"`

	// Channels with unread info
	Channels []struct {
		ID                 string          `json:"id"`
		Name               string          `json:"name"`
		IsChannel          bool            `json:"is_channel"`
		IsGroup            bool            `json:"is_group"`
		IsIM               bool            `json:"is_im"`
		IsMpim             bool            `json:"is_mpim"`
		UnreadCount        int             `json:"unread_count"`
		UnreadCountDisplay int             `json:"unread_count_display"`
		LastRead           string          `json:"last_read"`
		Latest             json.RawMessage `json:"latest"`
	} `json:"channels"`

	// Direct messages
	IMs []struct {
		ID          string `json:"id"`
		User        string `json:"user"`
		Latest      string `json:"latest"`
		UnreadCount int    `json:"unread_count"`
		IsOpen      bool   `json:"is_open"`
	} `json:"ims"`
}

// GetClientBoot fetches initial client state including unread counts
func (c *InternalClient) GetClientBoot(ctx context.Context) (*ClientBootResponse, error) {
	params := url.Values{
		"flannel":              {"1"},
		"no_latest":            {"0"},
		"batch_presence_aware": {"1"},
	}

	result := &ClientBootResponse{}
	err := c.callInternalAPI(ctx, "/api/client.boot", params, result)
	return result, err
}

// SearchModulesResponse represents search results from internal search
type SearchModulesResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	Messages struct {
		Total   int `json:"total"`
		Results []struct {
			Type    string `json:"type"`
			Channel struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channel"`
			User      string `json:"user"`
			Text      string `json:"text"`
			Timestamp string `json:"ts"`
			Permalink string `json:"permalink"`
		} `json:"matches"`
	} `json:"messages"`
}

// SearchMessages uses the internal search.modules endpoint
func (c *InternalClient) SearchMessages(ctx context.Context, query string, extraFilters map[string]string) (*SearchModulesResponse, error) {
	params := url.Values{
		"query":     {query},
		"module":    {"messages"},
		"count":     {"20"},
		"highlight": {"1"},
		"sort":      {"timestamp"},
		"sort_dir":  {"desc"},
	}

	// Add any extra filters
	for k, v := range extraFilters {
		params.Set(k, v)
	}

	result := &SearchModulesResponse{}
	err := c.callInternalAPI(ctx, "/api/search.modules", params, result)
	return result, err
}

// callInternalAPI is a helper to call internal Slack endpoints
func (c *InternalClient) callInternalAPI(ctx context.Context, endpoint string, params url.Values, result interface{}) error {
	// Build URL
	u, err := url.Parse(c.baseURL + endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint: %w", err)
	}

	if params != nil {
		u.RawQuery = params.Encode()
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	// Set headers to mimic browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.xoxcToken))
	req.Header.Set("Cookie", fmt.Sprintf("d=%s", c.xoxdToken))
	req.Header.Set("Origin", "https://app.slack.com")
	req.Header.Set("Referer", "https://app.slack.com/")

	// Make request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("making request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	// Check status
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Parse JSON
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	return nil
}

// PostInternalAPI calls internal endpoints with POST method
func (c *InternalClient) PostInternalAPI(ctx context.Context, endpoint string, payload interface{}, result interface{}) error {
	// Encode payload
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding payload: %w", err)
	}

	// Build URL
	u := c.baseURL + endpoint

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	// Set headers
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.xoxcToken))
	req.Header.Set("Cookie", fmt.Sprintf("d=%s", c.xoxdToken))
	req.Header.Set("Origin", "https://app.slack.com")
	req.Header.Set("Referer", "https://app.slack.com/")

	// Make request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("making request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	// Check status
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Parse JSON
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	return nil
}
