package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"github.com/aaronsb/slack-mcp/pkg/transport"
	"github.com/slack-go/slack"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type ApiProvider struct {
	boot           func() *slack.Client
	client         *slack.Client
	internalClient *InternalClient

	users         map[string]slack.User
	usersCache    string
	channels      map[string]slack.Channel // Channel ID -> Channel info
	channelNames  map[string]string        // Channel name -> Channel ID
	channelsMutex sync.RWMutex

	// Cache management
	lastChannelRefresh time.Time
	refreshCalls       int
	refreshResetTime   time.Time
}

func New() *ApiProvider {
	token := os.Getenv("SLACK_MCP_XOXC_TOKEN")
	if token == "" {
		panic("SLACK_MCP_XOXC_TOKEN environment variable is required")
	}

	cookie := os.Getenv("SLACK_MCP_XOXD_TOKEN")
	if cookie == "" {
		panic("SLACK_MCP_XOXD_TOKEN environment variable is required")
	}

	cache := os.Getenv("SLACK_MCP_USERS_CACHE")
	if cache == "" {
		cache = ".users_cache.json"
	}

	return &ApiProvider{
		boot: func() *slack.Client {
			api := slack.New(token,
				withHTTPClientOption(cookie),
			)
			res, err := api.AuthTest()
			if err != nil {
				panic(err)
			} else {
				log.Printf("Authenticated as: %s\n", res)
			}

			api = slack.New(token,
				withHTTPClientOption(cookie),
				withTeamEndpointOption(res.URL),
			)

			return api
		},
		internalClient: NewInternalClient(token, cookie),
		users:          make(map[string]slack.User),
		usersCache:     cache,
		channels:       make(map[string]slack.Channel),
		channelNames:   make(map[string]string),
	}
}

func (ap *ApiProvider) Provide() (*slack.Client, error) {
	if ap.client == nil {
		ap.client = ap.boot()

		err := ap.bootstrapDependencies(context.Background())
		if err != nil {
			return nil, err
		}
	}

	return ap.client, nil
}

func (ap *ApiProvider) bootstrapDependencies(ctx context.Context) error {
	// Load users cache
	if data, err := ioutil.ReadFile(ap.usersCache); err == nil {
		var cachedUsers []slack.User
		if err := json.Unmarshal(data, &cachedUsers); err != nil {
			log.Printf("Failed to unmarshal %s: %v; will refetch", ap.usersCache, err)
		} else {
			for _, u := range cachedUsers {
				ap.users[u.ID] = u
			}
			log.Printf("Loaded %d users from cache %q", len(cachedUsers), ap.usersCache)

			// Still need to load channels even if users are cached
			return ap.loadChannelsProgressive(ctx)
		}
	}

	// Fetch users
	optionLimit := slack.GetUsersOptionLimit(1000)
	users, err := ap.client.GetUsersContext(ctx,
		optionLimit,
	)
	if err != nil {
		log.Printf("Failed to fetch users: %v", err)
		return err
	}

	for _, user := range users {
		ap.users[user.ID] = user
	}

	if data, err := json.MarshalIndent(users, "", "  "); err != nil {
		log.Printf("Failed to marshal users for cache: %v", err)
	} else {
		if err := ioutil.WriteFile(ap.usersCache, data, 0644); err != nil {
			log.Printf("Failed to write cache file %q: %v", ap.usersCache, err)
		} else {
			log.Printf("Wrote %d users to cache %q", len(users), ap.usersCache)
		}
	}

	// Load channels progressively
	return ap.loadChannelsProgressive(ctx)
}

// loadChannelsProgressive loads channels progressively without blocking
func (ap *ApiProvider) loadChannelsProgressive(ctx context.Context) error {
	log.Println("Loading channel cache progressively...")

	// Try to load from cache file first
	cacheFile := ".channels_cache.json"
	if data, err := ioutil.ReadFile(cacheFile); err == nil {
		var cachedChannels []slack.Channel
		if err := json.Unmarshal(data, &cachedChannels); err == nil {
			// Load from cache
			ap.channelsMutex.Lock()
			ap.channels = make(map[string]slack.Channel)
			ap.channelNames = make(map[string]string)

			for _, ch := range cachedChannels {
				ap.channels[ch.ID] = ch

				// Build name mappings
				if ch.Name != "" {
					ap.channelNames[ch.Name] = ch.ID
					ap.channelNames[strings.ToLower(ch.Name)] = ch.ID
				}

				// For DMs, also map by user's real name
				if ch.IsIM && ch.User != "" {
					if user, ok := ap.users[ch.User]; ok {
						if user.RealName != "" {
							ap.channelNames[user.RealName] = ch.ID
							ap.channelNames[strings.ToLower(user.RealName)] = ch.ID
						}
						if user.Name != "" {
							ap.channelNames[user.Name] = ch.ID
							ap.channelNames[strings.ToLower(user.Name)] = ch.ID
						}
					}
				}
			}

			ap.lastChannelRefresh = time.Now()
			ap.channelsMutex.Unlock()

			log.Printf("Loaded %d channels from cache %q", len(cachedChannels), cacheFile)

			// Still fetch fresh data in background
			go ap.refreshChannelsInBackground(ctx)
			return nil
		}
	}

	// No cache, start fresh
	ap.channelsMutex.Lock()
	ap.channels = make(map[string]slack.Channel)
	ap.channelNames = make(map[string]string)
	ap.channelsMutex.Unlock()

	// Load channels in background
	go ap.refreshChannelsInBackground(ctx)

	return nil
}

// refreshChannelsInBackground fetches channels from API progressively
func (ap *ApiProvider) refreshChannelsInBackground(ctx context.Context) {
	log.Println("Starting background channel refresh...")

	// Phase 1: Load channels user is member of (fast)
	log.Println("Phase 1: Loading member channels...")
	memberCursor := ""
	memberCount := 0

	for {
		channels, nextCursor, err := ap.client.GetConversationsForUser(&slack.GetConversationsForUserParameters{
			Cursor:          memberCursor,
			Limit:           100,
			Types:           []string{"public_channel", "private_channel", "mpim", "im"},
			ExcludeArchived: true,
		})
		if err != nil {
			if rateLimitErr, ok := err.(*slack.RateLimitedError); ok {
				log.Printf("Rate limited, waiting %v before retry", rateLimitErr.RetryAfter)
				time.Sleep(rateLimitErr.RetryAfter)
				continue
			}
			log.Printf("Failed to fetch member channels: %v", err)
			break
		}

		// Add member channels to cache
		ap.channelsMutex.Lock()
		for _, ch := range channels {
			// These are channels user is member of
			ch.IsMember = true
			ap.channels[ch.ID] = ch

			// Build name mappings
			if ch.Name != "" {
				ap.channelNames[ch.Name] = ch.ID
				ap.channelNames[strings.ToLower(ch.Name)] = ch.ID
			}

			// For DMs, also map by user's real name
			if ch.IsIM && ch.User != "" {
				if user, ok := ap.users[ch.User]; ok {
					if user.RealName != "" {
						ap.channelNames[user.RealName] = ch.ID
						ap.channelNames[strings.ToLower(user.RealName)] = ch.ID
					}
					if user.Name != "" {
						ap.channelNames[user.Name] = ch.ID
						ap.channelNames[strings.ToLower(user.Name)] = ch.ID
					}
				}
			}
		}
		ap.channelsMutex.Unlock()

		memberCount += len(channels)
		log.Printf("Loaded %d member channels (total: %d)", len(channels), memberCount)

		if nextCursor == "" {
			break
		}
		memberCursor = nextCursor
		time.Sleep(500 * time.Millisecond) // Small delay between batches
	}

	log.Printf("Phase 1 complete: Loaded %d member channels", memberCount)

	// Phase 2: Load all other channels (slower, in background)
	log.Println("Phase 2: Loading all remaining channels...")
	cursor := ""
	totalLoaded := 0
	batchCount := 0

	for {
		channels, nextCursor, err := ap.client.GetConversations(&slack.GetConversationsParameters{
			Cursor: cursor,
			Limit:  100,
			Types:  []string{"public_channel", "private_channel", "mpim", "im"},
		})
		if err != nil {
			if rateLimitErr, ok := err.(*slack.RateLimitedError); ok {
				log.Printf("Rate limited, waiting %v before retry", rateLimitErr.RetryAfter)
				time.Sleep(rateLimitErr.RetryAfter)
				continue
			}
			log.Printf("Failed to fetch all channels: %v", err)
			return
		}

		// Add channels to cache progressively (lock per batch)
		ap.channelsMutex.Lock()
		for _, ch := range channels {
			// Only add if not already in cache (preserves IsMember status from Phase 1)
			if _, exists := ap.channels[ch.ID]; !exists {
				// These are channels from general list - user may or may not be member
				ap.channels[ch.ID] = ch

				// Build name mappings (only if name not already mapped)
				if ch.Name != "" {
					if _, nameExists := ap.channelNames[ch.Name]; !nameExists {
						ap.channelNames[ch.Name] = ch.ID
					}
					if _, nameExists := ap.channelNames[strings.ToLower(ch.Name)]; !nameExists {
						ap.channelNames[strings.ToLower(ch.Name)] = ch.ID
					}
				}
			}
		}
		ap.channelsMutex.Unlock()

		totalLoaded += len(channels)
		batchCount++

		log.Printf("Phase 2: Loaded %d channels (total: %d)", len(channels), totalLoaded)

		if nextCursor == "" {
			break
		}
		cursor = nextCursor

		// Add a small delay between batches to avoid rate limits
		if batchCount%3 == 0 {
			log.Printf("Pausing to avoid rate limits...")
			time.Sleep(2 * time.Second)
		}
	}

	log.Printf("Background refresh complete: %d channels loaded", totalLoaded)

	// Save to cache file
	ap.channelsMutex.RLock()
	cachedChannels := make([]slack.Channel, 0, len(ap.channels))
	for _, ch := range ap.channels {
		cachedChannels = append(cachedChannels, ch)
	}
	ap.lastChannelRefresh = time.Now()
	ap.channelsMutex.RUnlock()

	if data, err := json.MarshalIndent(cachedChannels, "", "  "); err != nil {
		log.Printf("Failed to marshal channels for cache: %v", err)
	} else {
		if err := ioutil.WriteFile(".channels_cache.json", data, 0644); err != nil {
			log.Printf("Failed to write channel cache file: %v", err)
		} else {
			log.Printf("Wrote %d channels to cache", len(cachedChannels))
		}
	}
}

func (ap *ApiProvider) ProvideUsersMap() map[string]slack.User {
	return ap.users
}

func (ap *ApiProvider) ProvideInternalClient() *InternalClient {
	return ap.internalClient
}

// GetChannelInfo gets channel info with caching
// It accepts both channel IDs and names
func (ap *ApiProvider) GetChannelInfo(ctx context.Context, channelIDOrName string) (*slack.Channel, error) {
	// First try to resolve name to ID
	channelID := channelIDOrName

	ap.channelsMutex.RLock()
	// Check if it's a name that needs resolution
	if !strings.HasPrefix(channelIDOrName, "C") && !strings.HasPrefix(channelIDOrName, "D") && !strings.HasPrefix(channelIDOrName, "G") {
		// Try to find by name
		if id, ok := ap.channelNames[channelIDOrName]; ok {
			channelID = id
		} else if id, ok := ap.channelNames[strings.ToLower(channelIDOrName)]; ok {
			channelID = id
		}
	}

	// Check cache
	if ch, ok := ap.channels[channelID]; ok {
		ap.channelsMutex.RUnlock()
		return &ch, nil
	}
	ap.channelsMutex.RUnlock()

	// Not in cache, fetch from API
	client, err := ap.Provide()
	if err != nil {
		return nil, err
	}

	info, err := client.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		return nil, err
	}

	// Cache the result
	ap.channelsMutex.Lock()
	ap.channels[channelID] = *info
	// Also update name mappings
	if info.Name != "" {
		ap.channelNames[info.Name] = info.ID
		ap.channelNames[strings.ToLower(info.Name)] = info.ID
	}
	ap.channelsMutex.Unlock()

	return info, nil
}

// ResolveChannelName resolves a channel ID to a name using cache
func (ap *ApiProvider) ResolveChannelName(ctx context.Context, channelID string) string {
	info, err := ap.GetChannelInfo(ctx, channelID)
	if err != nil {
		return channelID // Return ID if can't resolve
	}
	return info.Name
}

// ResolveChannelID resolves a channel name to ID
// Returns the ID if found, or the original input if not
func (ap *ApiProvider) ResolveChannelID(channelNameOrID string) string {
	// If it already looks like an ID, return it
	if strings.HasPrefix(channelNameOrID, "C") || strings.HasPrefix(channelNameOrID, "D") || strings.HasPrefix(channelNameOrID, "G") {
		return channelNameOrID
	}

	ap.channelsMutex.RLock()
	defer ap.channelsMutex.RUnlock()

	// Try exact match
	if id, ok := ap.channelNames[channelNameOrID]; ok {
		return id
	}

	// Try lowercase match
	if id, ok := ap.channelNames[strings.ToLower(channelNameOrID)]; ok {
		return id
	}

	// Not found, return original
	return channelNameOrID
}

// RefreshResult contains information about a cache refresh attempt
type RefreshResult struct {
	Allowed      bool
	LastRefresh  time.Time
	RetryAfter   time.Duration
	RefreshCount int
}

// CacheInfo contains information about the channel cache
type CacheInfo struct {
	LastRefresh  time.Time
	ChannelCount int
	RefreshCount int
}

// RefreshChannelCache refreshes the channel cache with rate limiting
func (ap *ApiProvider) RefreshChannelCache(ctx context.Context) (*RefreshResult, error) {
	ap.channelsMutex.Lock()
	defer ap.channelsMutex.Unlock()

	now := time.Now()

	// Reset call counter if window expired (5 minutes)
	if now.Sub(ap.refreshResetTime) > 5*time.Minute {
		ap.refreshCalls = 0
		ap.refreshResetTime = now
	}

	// Rate limiting logic:
	// - Minimum 30 seconds between refreshes
	// - Maximum 3 refreshes per 5-minute window
	// - Backoff: each call within window adds 30 seconds to minimum wait

	minWait := 30*time.Second + time.Duration(ap.refreshCalls)*30*time.Second
	timeSinceLastRefresh := now.Sub(ap.lastChannelRefresh)

	if timeSinceLastRefresh < minWait || ap.refreshCalls >= 3 {
		retryAfter := minWait - timeSinceLastRefresh
		if retryAfter < 0 {
			retryAfter = 30 * time.Second
		}

		return &RefreshResult{
			Allowed:      false,
			LastRefresh:  ap.lastChannelRefresh,
			RetryAfter:   retryAfter,
			RefreshCount: ap.refreshCalls,
		}, nil
	}

	// Allowed to refresh
	ap.refreshCalls++

	// Start background refresh
	go ap.refreshChannelsInBackground(ctx)

	ap.lastChannelRefresh = now

	return &RefreshResult{
		Allowed:      true,
		LastRefresh:  now,
		RetryAfter:   0,
		RefreshCount: ap.refreshCalls,
	}, nil
}

// GetCachedChannels returns all cached channels
func (ap *ApiProvider) GetCachedChannels() []slack.Channel {
	ap.channelsMutex.RLock()
	defer ap.channelsMutex.RUnlock()

	channels := make([]slack.Channel, 0, len(ap.channels))
	for _, ch := range ap.channels {
		channels = append(channels, ch)
	}

	return channels
}

// GetCacheInfo returns information about the channel cache
func (ap *ApiProvider) GetCacheInfo() CacheInfo {
	ap.channelsMutex.RLock()
	defer ap.channelsMutex.RUnlock()

	return CacheInfo{
		LastRefresh:  ap.lastChannelRefresh,
		ChannelCount: len(ap.channels),
		RefreshCount: ap.refreshCalls,
	}
}

func withHTTPClientOption(cookie string) func(c *slack.Client) {
	return func(c *slack.Client) {
		var proxy func(*http.Request) (*url.URL, error)
		if proxyURL := os.Getenv("SLACK_MCP_PROXY"); proxyURL != "" {
			parsed, err := url.Parse(proxyURL)
			if err != nil {
				log.Fatalf("Failed to parse proxy URL: %v", err)
			}

			proxy = http.ProxyURL(parsed)
		} else {
			proxy = nil
		}

		rootCAs, _ := x509.SystemCertPool()
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}

		if localCertFile := os.Getenv("SLACK_MCP_SERVER_CA"); localCertFile != "" {
			certs, err := ioutil.ReadFile(localCertFile)
			if err != nil {
				log.Fatalf("Failed to append %q to RootCAs: %v", localCertFile, err)
			}

			if ok := rootCAs.AppendCertsFromPEM(certs); !ok {
				log.Println("No certs appended, using system certs only")
			}
		}

		insecure := false
		if os.Getenv("SLACK_MCP_SERVER_CA_INSECURE") != "" {
			if localCertFile := os.Getenv("SLACK_MCP_SERVER_CA"); localCertFile != "" {
				log.Fatalf("Variable SLACK_MCP_SERVER_CA is at the same time with SLACK_MCP_SERVER_CA_INSECURE")
			}
			insecure = true
		}

		customHTTPTransport := &http.Transport{
			Proxy: proxy,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: insecure,
				RootCAs:            rootCAs,
			},
		}

		client := &http.Client{
			Transport: transport.New(
				customHTTPTransport,
				"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
				cookie,
			),
		}

		slack.OptionHTTPClient(client)(c)
	}
}

func withTeamEndpointOption(url string) slack.Option {
	return func(c *slack.Client) {
		slack.OptionAPIURL(url + "api/")(c)
	}
}
