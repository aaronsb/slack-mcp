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
	"github.com/aaronsb/slack-mcp/pkg/estate"
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
	bootOnce       sync.Once
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

	// Authenticated user identity
	selfUserID string
	selfUser   string
	selfTeam   string
	selfTeamID string

	// Cache persistence
	store *cache.Store

	// Estate ledger (ADR-007): nil when the ledger could not open, and
	// every call site degrades on nil. estateMu guards the pointer itself:
	// the background boot goroutine assigns it while handler goroutines
	// read it through est().
	estate              *estate.Store
	estateMu            sync.RWMutex
	estateSweepInterval time.Duration
	estateSweepStop     chan struct{}

	// Live channel-walk progress, so coverage reporting can show the
	// mapping advancing instead of a bare "not swept yet".
	walkMu      sync.Mutex
	channelWalk WalkProgress

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

// Option configures an ApiProvider at construction.
type Option func(*providerConfig)

type providerConfig struct {
	// baseURL replaces https://slack.com for both the slack-go client and the
	// internal client. Set it to point at a fake in tests, or at a custom
	// endpoint. Empty means normal team-endpoint discovery via auth.test.
	baseURL string

	// estateSweepInterval overrides the 24h estate sweep cadence. Zero
	// keeps the default. A construction option only — no env var and no
	// CLI flag, because the estate maintains itself.
	estateSweepInterval time.Duration
}

// WithEstateSweepInterval overrides how often the estate sweep runs. Meant
// for tests; production uses the default.
func WithEstateSweepInterval(d time.Duration) Option {
	return func(c *providerConfig) { c.estateSweepInterval = d }
}

// WithBaseURL overrides the Slack host for the API requests this provider
// makes, on both the slack-go client and the internal client. Supplying it also
// skips team-endpoint discovery, since the caller has already named the
// endpoint — which means the auth.test round trip that would otherwise validate
// the host does not run.
//
// File downloads are NOT redirected: DownloadFile pins files.slack.com and
// slack.com subdomains independently of this option.
//
// Every redirected request carries the xoxc bearer token and the xoxd cookie.
// Pass only a host you control — a test fake or a known endpoint — and never a
// value derived from user input, configuration, or the environment.
func WithBaseURL(u string) Option {
	return func(c *providerConfig) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// NewWithTokens creates a provider with explicit tokens
func NewWithTokens(token, cookie string, opts ...Option) *ApiProvider {
	var cfg providerConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	// Initialize XDG cache store
	store, err := cache.NewStore()
	if err != nil {
		log.Printf("Warning: could not create cache store: %v", err)
	}

	internal := NewInternalClient(token, cookie)
	if cfg.baseURL != "" {
		internal.baseURL = cfg.baseURL
	}

	ap := &ApiProvider{
		boot: func() *slack.Client {
			if cfg.baseURL != "" {
				return slack.New(token,
					withHTTPClientOption(cookie),
					slack.OptionAPIURL(cfg.baseURL+"/api/"),
				)
			}

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
		internalClient:      internal,
		users:               make(map[string]slack.User),
		channels:            make(map[string]slack.Channel),
		channelNames:        make(map[string]string),
		dmMap:               make(map[string]string),
		store:               store,
		estateSweepInterval: cfg.estateSweepInterval,
	}

	return ap
}

func (ap *ApiProvider) Provide() (*slack.Client, error) {
	var bootErr error
	ap.bootOnce.Do(func() {
		ap.client = ap.boot()
		ap.captureIdentity()
		bootErr = ap.bootstrapDependencies(context.Background())
	})
	if bootErr != nil {
		return nil, bootErr
	}
	return ap.client, nil
}

func (ap *ApiProvider) captureIdentity() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := ap.client.AuthTestContext(ctx)
	if err != nil {
		log.Printf("Could not capture identity: %v", err)
		return
	}
	ap.selfUserID = res.UserID
	ap.selfUser = res.User
	ap.selfTeam = res.Team
	ap.selfTeamID = res.TeamID
}

func (ap *ApiProvider) bootstrapDependencies(ctx context.Context) error {
	// Migrate old CWD cache files to XDG
	if ap.store != nil {
		ap.store.MigrateFromCWD(map[string]string{
			".users_cache.json":    usersCacheFile,
			".channels_cache.json": channelsCacheFile,
		})
	}

	// Open the estate ledger before any fetch path runs, so every complete
	// enumeration below is observed. captureIdentity has already run.
	ap.openEstate()

	// Load users from cache
	snapshotUsers := ap.loadUsersFromCache()

	if snapshotUsers == 0 {
		// No users on disk. Estate hydration may have filled the map with
		// skeletons, but a wiped snapshot is a request for fresh data — the
		// gate keys on the snapshot, not the map, or wiping the cache would
		// no longer force a refresh.
		if err := ap.fetchAndCacheUsers(ctx); err != nil {
			log.Printf("Failed to fetch users: %v", err)
			return err
		}
	}

	// Load channels from cache
	ap.loadChannelsFromCache()

	// Fetch member channels (fast — only channels user belongs to)
	go ap.loadMemberChannels(ctx)

	// Start background backfill on relaxed schedule, unless the estate
	// proves a recent complete enumeration — the watermark survives
	// restarts, so a bounced server stops re-walking the workspace.
	go ap.backfillIfStale(ctx)

	// Start periodic cache flush
	if ap.store != nil {
		ap.store.StartPeriodicFlush(flushInterval, ap.flushCaches)
	}

	// Self-schedule the estate sweep (ADR-007)
	ap.startEstateSweepScheduler(ctx)

	return nil
}

// loadUsersFromCache loads users from the XDG snapshot, with the estate
// fold as the authority on existence (ADR-007): a tombstoned user loads
// with Deleted forced so the render and filter paths see the fact, and a
// fold-live user the snapshot lost hydrates as a skeleton — a deleted or
// corrupt snapshot self-heals instead of needing a hand-deleted file.
func (ap *ApiProvider) loadUsersFromCache() int {
	var cachedUsers []slack.User
	if ap.store != nil {
		if err := ap.store.Load(usersCacheFile, &cachedUsers); err != nil {
			cachedUsers = nil
		}
	}

	var estateUsers map[string]estate.UserRecord
	if st := ap.est(); st != nil {
		estateUsers = st.Users()
	}

	hydrated := 0
	ap.usersMutex.Lock()
	for _, u := range cachedUsers {
		if rec, ok := estateUsers[u.ID]; ok && rec.Gone != nil {
			u.Deleted = true
		}
		ap.users[u.ID] = u
	}
	for id, rec := range estateUsers {
		if rec.Gone != nil {
			continue
		}
		if _, ok := ap.users[id]; !ok {
			ap.users[id] = estate.HydrateUser(rec)
			hydrated++
		}
	}
	ap.usersMutex.Unlock()

	if hydrated > 0 {
		log.Printf("Loaded %d users from cache, hydrated %d from the estate", len(cachedUsers), hydrated)
	} else {
		log.Printf("Loaded %d users from cache", len(cachedUsers))
	}
	return len(cachedUsers)
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

	// users.list paginated to exhaustion is a complete enumeration, so the
	// estate may assert user absences from it.
	res := ap.observeUsersEstate(users, true, estate.SourceBoot)
	ap.recordEstateSweep(estate.SweepReport{
		Users:    estate.ClassReport{Complete: true, Count: len(users), AbsenceAborted: res.AbsenceAborted},
		Appended: res.Appended,
	})

	return nil
}

// loadChannelsFromCache loads channels from the XDG snapshot, with the
// estate fold as the authority on existence (ADR-007): a tombstoned channel
// stays out of the live map — its dated absence is served by the estate
// read APIs, not by a silent gap in a listing — and a fold-live channel the
// snapshot lost hydrates as a skeleton.
func (ap *ApiProvider) loadChannelsFromCache() {
	var cachedChannels []slack.Channel
	if ap.store != nil {
		if err := ap.store.Load(channelsCacheFile, &cachedChannels); err != nil {
			cachedChannels = nil
		}
	}

	var estateChannels map[string]estate.ChannelRecord
	if st := ap.est(); st != nil {
		estateChannels = st.Channels()
	}

	loaded := make([]slack.Channel, 0, len(cachedChannels))
	loadedIDs := make(map[string]bool, len(cachedChannels))
	for _, ch := range cachedChannels {
		if rec, ok := estateChannels[ch.ID]; ok && rec.Gone != nil {
			continue
		}
		loaded = append(loaded, ch)
		loadedIDs[ch.ID] = true
	}
	hydrated := 0
	for id, rec := range estateChannels {
		if rec.Gone != nil || loadedIDs[id] {
			continue
		}
		loaded = append(loaded, estate.HydrateChannel(rec))
		hydrated++
	}

	ap.channelsMutex.Lock()
	for _, ch := range loaded {
		ap.channels[ch.ID] = ch
		ap.indexChannel(ch)
	}
	ap.lastChannelRefresh = time.Now()
	ap.channelsMutex.Unlock()

	// Index DM mappings outside channelsMutex
	for _, ch := range loaded {
		ap.indexChannelDM(ch)
	}

	// Load DM map
	if ap.store != nil {
		var dmMap map[string]string
		if err := ap.store.Load(dmMapCacheFile, &dmMap); err == nil {
			ap.dmMapMutex.Lock()
			ap.dmMap = dmMap
			ap.dmMapMutex.Unlock()
		}
	}

	if excluded := len(cachedChannels) + hydrated - len(loaded); excluded > 0 || hydrated > 0 {
		log.Printf("Loaded %d channels from cache (%d tombstoned excluded, %d hydrated from the estate)",
			len(loaded), excluded, hydrated)
		return
	}
	log.Printf("Loaded %d channels from cache", len(loaded))
}

// indexChannel adds name mappings for a channel (caller must hold channelsMutex write lock)
func (ap *ApiProvider) indexChannel(ch slack.Channel) {
	if ch.Name != "" {
		ap.channelNames[ch.Name] = ch.ID
		ap.channelNames[strings.ToLower(ch.Name)] = ch.ID
	}

	// For DMs, map by user's real name and username.
	// Note: we do NOT acquire usersMutex or dmMapMutex here to avoid
	// lock ordering violations. Callers should call indexChannelDM()
	// separately after releasing channelsMutex.
	// The name mappings for DMs are best-effort from cached user data.
}

// indexChannelDM adds DM-specific mappings (user name → channel ID, DM map).
// Must be called WITHOUT channelsMutex held to avoid lock ordering issues.
func (ap *ApiProvider) indexChannelDM(ch slack.Channel) {
	if !ch.IsIM || ch.User == "" {
		return
	}

	ap.usersMutex.RLock()
	user, ok := ap.users[ch.User]
	ap.usersMutex.RUnlock()

	if ok {
		ap.channelsMutex.Lock()
		if user.RealName != "" {
			ap.channelNames[user.RealName] = ch.ID
			ap.channelNames[strings.ToLower(user.RealName)] = ch.ID
		}
		if user.Name != "" {
			ap.channelNames[user.Name] = ch.ID
			ap.channelNames[strings.ToLower(user.Name)] = ch.ID
		}
		ap.channelsMutex.Unlock()
	}

	ap.dmMapMutex.Lock()
	ap.dmMap[ch.User] = ch.ID
	ap.dmMapMutex.Unlock()
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
		for i := range channels {
			channels[i].IsMember = true
			ap.channels[channels[i].ID] = channels[i]
			ap.indexChannel(channels[i])
		}
		ap.channelsMutex.Unlock()

		for _, ch := range channels {
			ap.indexChannelDM(ch)
		}

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
	appended := 0
	walk := ap.fetchAllChannels(ctx, func(page []slack.Channel) {
		// Upsert, preserving the map's IsMember (member-load is the boot
		// authority on membership), so channel renames and archives are
		// re-observed instead of frozen behind an add-only merge. Observe
		// each page as it lands (asserting no absences), so an interrupted
		// walk's knowledge is already durable and the resumed walk appends
		// nothing for what it re-sees.
		merged := ap.mergeChannels(page, nil)
		res := ap.observeChannelsEstate(merged, false, estate.SourceBoot)
		appended += res.Appended
	})

	if !walk.complete {
		// Walk failed partway: merged pages stay, the checkpoint stays for
		// the next attempt, backfillDone stays false so RefreshChannelCache
		// can retry, and no absences were asserted.
		return
	}

	ap.backfillMutex.Lock()
	ap.backfillDone = true
	ap.backfillMutex.Unlock()

	res := ap.closeChannelEnumerationEstate(walk.seen, estate.SourceBoot, walk.startedAt)
	ap.recordEstateSweep(estate.SweepReport{
		Channels: estate.ClassReport{
			Complete: true, Count: len(walk.seen),
			ArchivedIncluded: true, AbsenceAborted: res.AbsenceAborted,
		},
		Appended: appended + res.Appended,
	})

	ap.reconcileEstateTombstones()
	log.Printf("Background backfill complete: %d channels", len(walk.seen))
	ap.markDirty()
	ap.flushCaches()
}

// mergeChannels upserts a fetched page into the map and returns the merged
// values (what estate observation must see, so both observers feed the
// ledger identical structs). Membership: when memberIDs is non-nil it is
// authoritative for unarchived channels; archived channels keep their
// fetched flag because the membership walk excludes them; when memberIDs is
// nil the map's existing IsMember survives the upsert.
func (ap *ApiProvider) mergeChannels(page []slack.Channel, memberIDs map[string]bool) []slack.Channel {
	merged := make([]slack.Channel, 0, len(page))
	var newDMs []slack.Channel
	ap.channelsMutex.Lock()
	for _, ch := range page {
		existing, exists := ap.channels[ch.ID]
		if memberIDs != nil {
			if !ch.IsArchived {
				ch.IsMember = memberIDs[ch.ID]
			}
		} else if exists {
			ch.IsMember = existing.IsMember
		}
		ap.channels[ch.ID] = ch
		ap.indexChannel(ch)
		if !exists && ch.IsIM {
			newDMs = append(newDMs, ch)
		}
		merged = append(merged, ch)
	}
	ap.channelsMutex.Unlock()

	for _, ch := range newDMs {
		ap.indexChannelDM(ch)
	}
	return merged
}

// fetchAllChannels walks conversations.list to cursor exhaustion — archived
// channels included, which ADR-007 makes a requirement rather than the
// accident it was. Each page is handed to onPage as it lands so channels
// stay progressively available, and the walk checkpoints its cursor and
// seen set per page so a killed process resumes instead of restarting from
// page one. Returns the channels fetched this process, the seen set across
// resumes, and whether the enumeration completed.
// channelWalkResult is what one enumeration attempt produced: the seen set
// across resumes, when the enumeration first started, and whether it
// completed.
type channelWalkResult struct {
	seen      map[string]bool
	startedAt time.Time
	complete  bool
}

func (ap *ApiProvider) fetchAllChannels(ctx context.Context, onPage func([]slack.Channel)) channelWalkResult {
	// Single walker: two concurrent enumerations would share one checkpoint
	// file and together run twice the rate tier — the paired 30s backoffs
	// observed live before the scheduler learned to wait.
	ap.walkMu.Lock()
	if ap.channelWalk.Active {
		ap.walkMu.Unlock()
		log.Printf("Channel walk already in progress; skipping this attempt")
		return channelWalkResult{seen: map[string]bool{}}
	}
	cursor, startedAt, seen, resumed := ap.loadWalkState()
	if !resumed {
		startedAt = time.Now()
		seen = make(map[string]bool)
	} else {
		log.Printf("Channel walk resuming: %d conversations already seen", len(seen))
	}
	ap.channelWalk = WalkProgress{Active: true, Started: startedAt, Seen: len(seen)}
	ap.walkMu.Unlock()
	defer func() {
		ap.walkMu.Lock()
		ap.channelWalk.Active = false
		ap.walkMu.Unlock()
	}()

	for {
		channels, nextCursor, err := ap.client.GetConversations(&slack.GetConversationsParameters{
			Cursor: cursor,
			Limit:  100,
			Types:  []string{"public_channel", "private_channel", "mpim", "im"},
		})
		if err != nil {
			if rateLimitErr, ok := err.(*slack.RateLimitedError); ok {
				log.Printf("Channel walk rate limited, waiting %v", rateLimitErr.RetryAfter)
				time.Sleep(rateLimitErr.RetryAfter)
				continue
			}
			if resumed && strings.Contains(err.Error(), "invalid_cursor") {
				// The checkpointed cursor expired. Restart the enumeration
				// clean — the resumed pages' knowledge is already in the
				// ledger, so only the API cost is lost.
				log.Printf("Channel walk cursor expired; restarting the enumeration")
				ap.clearWalkState()
				cursor, resumed = "", false
				startedAt = time.Now()
				seen = make(map[string]bool)
				continue
			}
			log.Printf("Channel walk failed: %v", err)
			return channelWalkResult{seen: seen, startedAt: startedAt}
		}

		for i := range channels {
			seen[channels[i].ID] = true
		}
		if onPage != nil {
			onPage(channels)
		}
		ap.walkMu.Lock()
		ap.channelWalk.Seen = len(seen)
		ap.walkMu.Unlock()

		if nextCursor == "" {
			ap.clearWalkState()
			return channelWalkResult{seen: seen, startedAt: startedAt, complete: true}
		}
		cursor = nextCursor
		ap.saveWalkState(cursor, startedAt, seen)

		// conversations.list is a ~20 req/min tier. The old 1s/3s pacing ran
		// ~36/min, so past page twenty every page ate a 30s penalty and a
		// 3,000-conversation walk took ~13 minutes; a flat 3s stays under
		// the limit and finishes the same walk in ~2.
		time.Sleep(3 * time.Second)
	}
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

// ProvideUsersMap returns a snapshot copy of the users map.
// Safe for callers to iterate without holding locks.
func (ap *ApiProvider) ProvideUsersMap() map[string]slack.User {
	ap.usersMutex.RLock()
	defer ap.usersMutex.RUnlock()
	copy := make(map[string]slack.User, len(ap.users))
	for k, v := range ap.users {
		copy[k] = v
	}
	return copy
}

// Identity returns information about the authenticated user
type Identity struct {
	UserID      string `json:"userId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Title       string `json:"title,omitempty"`
	Email       string `json:"email,omitempty"`
	Team        string `json:"team"`
	TeamID      string `json:"teamId"`
}

// ProvideIdentity returns the authenticated user's identity
func (ap *ApiProvider) ProvideIdentity() *Identity {
	if ap.selfUserID == "" {
		return nil
	}

	id := &Identity{
		UserID:   ap.selfUserID,
		Username: ap.selfUser,
		Team:     ap.selfTeam,
		TeamID:   ap.selfTeamID,
	}

	// Enrich from users cache
	ap.usersMutex.RLock()
	if user, ok := ap.users[ap.selfUserID]; ok {
		if user.RealName != "" {
			id.DisplayName = user.RealName
		}
		if user.Profile.Title != "" {
			id.Title = user.Profile.Title
		}
		if user.Profile.Email != "" {
			id.Email = user.Profile.Email
		}
	}
	ap.usersMutex.RUnlock()

	if id.DisplayName == "" {
		id.DisplayName = id.Username
	}

	return id
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

	ap.indexChannelDM(*dmChannel)
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

	ap.indexChannelDM(*info)
	ap.observeChannelsEstate([]slack.Channel{*info}, false, estate.SourceTraffic)
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
	ap.observeUsersEstate([]slack.User{*user}, false, estate.SourceTraffic)
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

// ResolveChannelNameCached returns a channel's name from the cache alone.
// Render paths resolve every tag from held state — a network call per
// mention is not affordable there.
func (ap *ApiProvider) ResolveChannelNameCached(id string) string {
	ap.channelsMutex.RLock()
	defer ap.channelsMutex.RUnlock()
	if ch, ok := ap.channels[id]; ok {
		return ch.Name
	}
	return ""
}
