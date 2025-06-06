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
	"sync"
)

type ApiProvider struct {
	boot           func() *slack.Client
	client         *slack.Client
	internalClient *InternalClient

	users         map[string]slack.User
	usersCache    string
	channels      map[string]slack.Channel  // Channel ID -> Channel info
	channelsMutex sync.RWMutex
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
	if data, err := ioutil.ReadFile(ap.usersCache); err == nil {
		var cachedUsers []slack.User
		if err := json.Unmarshal(data, &cachedUsers); err != nil {
			log.Printf("Failed to unmarshal %s: %v; will refetch", ap.usersCache, err)
		} else {
			for _, u := range cachedUsers {
				ap.users[u.ID] = u
			}
			log.Printf("Loaded %d users from cache %q", len(cachedUsers), ap.usersCache)
			return nil
		}
	}

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

	return nil
}

func (ap *ApiProvider) ProvideUsersMap() map[string]slack.User {
	return ap.users
}

func (ap *ApiProvider) ProvideInternalClient() *InternalClient {
	return ap.internalClient
}

// GetChannelInfo gets channel info with caching
func (ap *ApiProvider) GetChannelInfo(ctx context.Context, channelID string) (*slack.Channel, error) {
	// Check cache first
	ap.channelsMutex.RLock()
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
