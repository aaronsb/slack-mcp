package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/cache"
	"github.com/aaronsb/slack-mcp/pkg/transport"
	"github.com/slack-go/slack"
)

// Cache file names in XDG data dir
const (
	channelsCacheFile = "channels.json"
	usersCacheFile    = "users.json"
	dmMapCacheFile    = "dm-map.json"
	flushInterval     = 5 * time.Minute
)

type ApiProvider struct {
	boot           func() *slack.Client
	client         *slack.Client
	internalClient *InternalClient

	users      map[string]slack.User
	usersMutex sync.RWMutex

	channels      map[string]slack.Channel // Channel ID -> Channel info
	channelNames  map[string]string        // Channel name/display name -> Channel ID
	channelsMutex sync.RWMutex

	// DM channel map: user name/ID -> DM channel ID
	dmMap      map[string]string
	dmMapMutex sync.RWMutex

	// Cache persistence
	store *cache.Store

	// Cache management
	lastChannelRefresh time.Time
	refreshCalls       int
	refreshResetTime   time.Time
	backfillDone       bool
	backfillMutex      sync.Mutex
}

// New creates a provider from environment variables (backward compatible)
func New() *ApiProvider {
	token := os.Getenv("SLACK_MCP_XOXC_TOKEN")
	if token == "" {
		panic("SLACK_MCP_XOXC_TOKEN environment variable is required")
	}

	cookie := os.Getenv("SLACK_MCP_XOXD_TOKEN")
	if cookie == "" {
		panic("SLACK_MCP_XOXD_TOKEN environment variable is required")
	}

	return NewWithTokens(token, cookie)
}

// NewWithTokens creates a provider with explicit tokens
func NewWithTokens(token, cookie string) *ApiProvider {
	// Initialize XDG cache store
	store, err := cache.NewStore()
	if err != nil {
		log.Printf("Warning: could not create cache store: %v", err)
	}

	ap := &ApiProvider{
		boot: func() *slack.Client {
			api := slack.New(token,
				withHTTPClientOption(cookie),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			res, err := api.AuthTestContext(ctx)
			if err != nil {
				log.Printf("ERROR: Slack authentication failed: %v", err)
				log.Printf("Please check your tokens")
				return api
			}

			log.Printf("Authenticated as: %s\n", res)

			api = slack.New(token,
				withHTTPClientOption(cookie),
				withTeamEndpointOption(res.URL),
			)

			return api
		},
		internalClient: NewInternalClient(token, cookie),
		users:          make(map[string]slack.User),
		channels:       make(map[string]slack.Channel),
		channelNames:   make(map[string]string),
		dmMap:          make(map[string]string),
		store:          store,
	}

	return ap
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
	// Migrate old CWD cache files to XDG
	if ap.store != nil {
		ap.store.MigrateFromCWD(map[string]string{
			".users_cache.json":    usersCacheFile,
			".channels_cache.json": channelsCacheFile,
		})
	}

	// Load users from cache
	ap.loadUsersFromCache()

	if len(ap.users) == 0 {
		// No cached users, fetch from API
		if err := ap.fetchAndCacheUsers(ctx); err != nil {
			log.Printf("Failed to fetch users: %v", err)
			return err
		}
	}

	// Load channels from cache
	ap.loadChannelsFromCache()

	// Fetch member channels (fast — only channels user belongs to)
	go ap.loadMemberChannels(ctx)

	// Start background backfill on relaxed schedule
	go ap.backgroundBackfill(ctx)

	// Start periodic cache flush
	if ap.store != nil {
		ap.store.StartPeriodicFlush(flushInterval, ap.flushCaches)
	}

	return nil
}

// loadUsersFromCache loads users from XDG cache file
func (ap *ApiProvider) loadUsersFromCache() {
	if ap.store == nil {
		return
	}

	var cachedUsers []slack.User
	if err := ap.store.Load(usersCacheFile, &cachedUsers); err != nil {
		return
	}

	ap.usersMutex.Lock()
	for _, u := range cachedUsers {
		ap.users[u.ID] = u
	}
	ap.usersMutex.Unlock()
	log.Printf("Loaded %d users from cache", len(cachedUsers))
}

// fetchAndCacheUsers fetches all users and saves to cache
func (ap *ApiProvider) fetchAndCacheUsers(ctx context.Context) error {
	users, err := ap.client.GetUsersContext(ctx, slack.GetUsersOptionLimit(1000))
	if err != nil {
		return err
	}

	ap.usersMutex.Lock()
	for _, user := range users {
		ap.users[user.ID] = user
	}
	ap.usersMutex.Unlock()

	if ap.store != nil {
		if err := ap.store.Save(usersCacheFile, users); err != nil {
			log.Printf("Failed to save users cache: %v", err)
		} else {
			log.Printf("Saved %d users to cache", len(users))
		}
	}

	return nil
}

// loadChannelsFromCache loads channels from XDG cache file
func (ap *ApiProvider) loadChannelsFromCache() {
	if ap.store == nil {
		return
	}

	var cachedChannels []slack.Channel
	if err := ap.store.Load(channelsCacheFile, &cachedChannels); err != nil {
		return
	}

	ap.channelsMutex.Lock()
	for _, ch := range cachedChannels {
		ap.channels[ch.ID] = ch
		ap.indexChannel(ch)
	}
	ap.lastChannelRefresh = time.Now()
	ap.channelsMutex.Unlock()

	// Load DM map
	var dmMap map[string]string
	if err := ap.store.Load(dmMapCacheFile, &dmMap); err == nil {
		ap.dmMapMutex.Lock()
		ap.dmMap = dmMap
		ap.dmMapMutex.Unlock()
	}

	log.Printf("Loaded %d channels from cache", len(cachedChannels))
}

// indexChannel adds name mappings for a channel (caller must hold channelsMutex write lock)
func (ap *ApiProvider) indexChannel(ch slack.Channel) {
	if ch.Name != "" {
		ap.channelNames[ch.Name] = ch.ID
		ap.channelNames[strings.ToLower(ch.Name)] = ch.ID
	}

	// For DMs, map by user's real name and username
	if ch.IsIM && ch.User != "" {
		ap.usersMutex.RLock()
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
		ap.usersMutex.RUnlock()

		// Also update DM map
		ap.dmMapMutex.Lock()
		ap.dmMap[ch.User] = ch.ID
		ap.dmMapMutex.Unlock()
	}
}

// loadMemberChannels fetches channels the user is a member of (fast startup)
func (ap *ApiProvider) loadMemberChannels(ctx context.Context) {
	log.Println("Loading member channels...")
	cursor := ""
	count := 0

	for {
		channels, nextCursor, err := ap.client.GetConversationsForUser(&slack.GetConversationsForUserParameters{
			Cursor:          cursor,
			Limit:           100,
			Types:           []string{"public_channel", "private_channel", "mpim", "im"},
			ExcludeArchived: true,
		})
		if err != nil {
			if rateLimitErr, ok := err.(*slack.RateLimitedError); ok {
				log.Printf("Rate limited, waiting %v", rateLimitErr.RetryAfter)
				time.Sleep(rateLimitErr.RetryAfter)
				continue
			}
			log.Printf("Failed to fetch member channels: %v", err)
			break
		}

		ap.channelsMutex.Lock()
		for _, ch := range channels {
			ch.IsMember = true
			ap.channels[ch.ID] = ch
			ap.indexChannel(ch)
		}
		ap.channelsMutex.Unlock()

		count += len(channels)

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
		time.Sleep(500 * time.Millisecond)
	}

	log.Printf("Loaded %d member channels", count)
	ap.markDirty()
}

// backgroundBackfill slowly loads remaining workspace channels
func (ap *ApiProvider) backgroundBackfill(ctx context.Context) {
	// Wait for member channels to load first
	time.Sleep(10 * time.Second)

	ap.backfillMutex.Lock()
	if ap.backfillDone {
		ap.backfillMutex.Unlock()
		return
	}
	ap.backfillMutex.Unlock()

	log.Println("Starting background channel backfill...")
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
				log.Printf("Backfill rate limited, waiting %v", rateLimitErr.RetryAfter)
				time.Sleep(rateLimitErr.RetryAfter)
				continue
			}
			log.Printf("Backfill failed: %v", err)
			return
		}

		ap.channelsMutex.Lock()
		for _, ch := range channels {
			if _, exists := ap.channels[ch.ID]; !exists {
				ap.channels[ch.ID] = ch
				ap.indexChannel(ch)
			}
		}
		ap.channelsMutex.Unlock()

		totalLoaded += len(channels)
		batchCount++

		if nextCursor == "" {
			break
		}
		cursor = nextCursor

		// Relaxed pacing: longer delays to avoid rate limits
		if batchCount%3 == 0 {
			time.Sleep(3 * time.Second)
		} else {
			time.Sleep(1 * time.Second)
		}
	}

	ap.backfillMutex.Lock()
	ap.backfillDone = true
	ap.backfillMutex.Unlock()

	log.Printf("Background backfill complete: %d channels", totalLoaded)
	ap.markDirty()
	ap.flushCaches()
}

// markDirty flags the cache store as needing a flush
func (ap *ApiProvider) markDirty() {
	if ap.store != nil {
		ap.store.MarkDirty()
	}
}

// flushCaches writes all in-memory caches to disk
func (ap *ApiProvider) flushCaches() error {
	if ap.store == nil {
		return nil
	}

	// Save channels
	ap.channelsMutex.RLock()
	channels := make([]slack.Channel, 0, len(ap.channels))
	for _, ch := range ap.channels {
		channels = append(channels, ch)
	}
	ap.channelsMutex.RUnlock()

	if err := ap.store.Save(channelsCacheFile, channels); err != nil {
		return fmt.Errorf("flush channels: %w", err)
	}

	// Save users
	ap.usersMutex.RLock()
	users := make([]slack.User, 0, len(ap.users))
	for _, u := range ap.users {
		users = append(users, u)
	}
	ap.usersMutex.RUnlock()

	if err := ap.store.Save(usersCacheFile, users); err != nil {
		return fmt.Errorf("flush users: %w", err)
	}

	// Save DM map
	ap.dmMapMutex.RLock()
	dmMapCopy := make(map[string]string, len(ap.dmMap))
	for k, v := range ap.dmMap {
		dmMapCopy[k] = v
	}
	ap.dmMapMutex.RUnlock()

	if err := ap.store.Save(dmMapCacheFile, dmMapCopy); err != nil {
		return fmt.Errorf("flush dm-map: %w", err)
	}

	log.Printf("Flushed caches: %d channels, %d users, %d DM mappings", len(channels), len(users), len(dmMapCopy))
	return nil
}

func (ap *ApiProvider) ProvideUsersMap() map[string]slack.User {
	ap.usersMutex.RLock()
	defer ap.usersMutex.RUnlock()
	return ap.users
}

func (ap *ApiProvider) ProvideInternalClient() *InternalClient {
	return ap.internalClient
}

// GetChannelInfo gets channel info with on-demand resolution.
// On cache miss, fetches from API and patches the cache.
func (ap *ApiProvider) GetChannelInfo(ctx context.Context, channelIDOrName string) (*slack.Channel, error) {
	channelID := channelIDOrName

	ap.channelsMutex.RLock()
	// Try name resolution if not already an ID
	if !looksLikeChannelID(channelIDOrName) {
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

	// Cache miss — try on-demand resolution

	// If the input doesn't look like a channel ID, try display name resolution
	if !looksLikeChannelID(channelIDOrName) {
		ch, err := ap.resolveByDisplayName(ctx, channelIDOrName)
		if err == nil {
			return ch, nil
		}
		log.Printf("Display name resolution failed for %q: %v", channelIDOrName, err)
	}

	// Try direct API fetch with the ID we have
	if looksLikeChannelID(channelID) {
		return ap.fetchAndCacheChannel(ctx, channelID)
	}

	return nil, fmt.Errorf("channel_not_found: %s", channelIDOrName)
}

// resolveByDisplayName tries to resolve a display name to a DM channel.
// It searches users by real name, then opens a DM via conversations.open.
func (ap *ApiProvider) resolveByDisplayName(ctx context.Context, name string) (*slack.Channel, error) {
	client, err := ap.Provide()
	if err != nil {
		return nil, err
	}

	// Search users for matching real name or username
	nameLower := strings.ToLower(name)

	ap.usersMutex.RLock()
	var matchedUserID string
	for _, user := range ap.users {
		if strings.ToLower(user.RealName) == nameLower || strings.ToLower(user.Name) == nameLower {
			matchedUserID = user.ID
			break
		}
	}
	ap.usersMutex.RUnlock()

	if matchedUserID == "" {
		return nil, fmt.Errorf("no user matching %q", name)
	}

	// Check DM map first
	ap.dmMapMutex.RLock()
	if dmID, ok := ap.dmMap[matchedUserID]; ok {
		ap.dmMapMutex.RUnlock()
		// We have the DM channel ID, fetch info
		ap.channelsMutex.RLock()
		if ch, ok := ap.channels[dmID]; ok {
			ap.channelsMutex.RUnlock()
			return &ch, nil
		}
		ap.channelsMutex.RUnlock()
		return ap.fetchAndCacheChannel(ctx, dmID)
	}
	ap.dmMapMutex.RUnlock()

	// Open DM conversation (creates if needed, returns existing if already open)
	dmChannel, _, _, err := client.OpenConversationContext(ctx, &slack.OpenConversationParameters{
		Users: []string{matchedUserID},
	})
	if err != nil {
		return nil, fmt.Errorf("open DM for %q: %w", name, err)
	}

	dmChannelID := dmChannel.ID

	// Patch DM map
	ap.dmMapMutex.Lock()
	ap.dmMap[matchedUserID] = dmChannelID
	ap.dmMapMutex.Unlock()

	// Cache the channel info we already have
	ap.channelsMutex.Lock()
	ap.channels[dmChannelID] = *dmChannel
	ap.indexChannel(*dmChannel)
	ap.channelNames[name] = dmChannelID
	ap.channelNames[nameLower] = dmChannelID
	ap.channelsMutex.Unlock()

	ap.markDirty()
	return dmChannel, nil
}

// fetchAndCacheChannel fetches a channel from API and patches the cache
func (ap *ApiProvider) fetchAndCacheChannel(ctx context.Context, channelID string) (*slack.Channel, error) {
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

	// Patch the cache
	ap.channelsMutex.Lock()
	ap.channels[channelID] = *info
	ap.indexChannel(*info)
	ap.channelsMutex.Unlock()

	ap.markDirty()
	return info, nil
}

// ResolveChannelName resolves a channel ID to a name using cache,
// fetching from API on cache miss.
func (ap *ApiProvider) ResolveChannelName(ctx context.Context, channelID string) string {
	info, err := ap.GetChannelInfo(ctx, channelID)
	if err != nil {
		return channelID
	}
	return info.Name
}

// ResolveChannelID resolves a channel name to ID.
// On cache miss, tries display name resolution via user search + conversations.open.
func (ap *ApiProvider) ResolveChannelID(channelNameOrID string) string {
	if looksLikeChannelID(channelNameOrID) {
		return channelNameOrID
	}

	ap.channelsMutex.RLock()
	if id, ok := ap.channelNames[channelNameOrID]; ok {
		ap.channelsMutex.RUnlock()
		return id
	}
	if id, ok := ap.channelNames[strings.ToLower(channelNameOrID)]; ok {
		ap.channelsMutex.RUnlock()
		return id
	}
	ap.channelsMutex.RUnlock()

	// Cache miss — try on-demand resolution
	ch, err := ap.resolveByDisplayName(context.Background(), channelNameOrID)
	if err != nil {
		log.Printf("ResolveChannelID: no match for %q: %v", channelNameOrID, err)
		return channelNameOrID
	}
	return ch.ID
}

// ResolveUser resolves a user ID to user info, fetching on cache miss.
func (ap *ApiProvider) ResolveUser(ctx context.Context, userID string) (*slack.User, error) {
	ap.usersMutex.RLock()
	if user, ok := ap.users[userID]; ok {
		ap.usersMutex.RUnlock()
		return &user, nil
	}
	ap.usersMutex.RUnlock()

	// Cache miss — fetch from API
	client, err := ap.Provide()
	if err != nil {
		return nil, err
	}

	user, err := client.GetUserInfoContext(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Patch cache
	ap.usersMutex.Lock()
	ap.users[user.ID] = *user
	ap.usersMutex.Unlock()
	ap.markDirty()

	return user, nil
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

	if now.Sub(ap.refreshResetTime) > 5*time.Minute {
		ap.refreshCalls = 0
		ap.refreshResetTime = now
	}

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

	ap.refreshCalls++
	ap.lastChannelRefresh = now

	// Reset backfill flag to allow re-backfill
	ap.backfillMutex.Lock()
	ap.backfillDone = false
	ap.backfillMutex.Unlock()

	go ap.backgroundBackfill(ctx)

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

// looksLikeChannelID returns true if the string looks like a Slack channel/DM/group ID
// looksLikeChannelID returns true if the string looks like a Slack channel/DM/group ID.
// Real IDs are a capital letter (C, D, G) followed by uppercase alphanumeric chars, no spaces.
func looksLikeChannelID(s string) bool {
	if len(s) < 2 {
		return false
	}
	if s[0] != 'C' && s[0] != 'D' && s[0] != 'G' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
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
			certs, err := os.ReadFile(localCertFile)
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
