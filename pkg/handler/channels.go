package handler

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/gocarina/gocsv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"log"
	"sort"
)

var AllChanTypes = []string{"mpim", "im", "public_channel", "private_channel"}
var PubChanType = []string{"public_channel"}

type Channel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Topic       string `json:"topic"`
	Purpose     string `json:"purpose"`
	MemberCount int    `json:"memberCount"`
}

type ChannelsHandler struct {
	apiProvider *provider.ApiProvider
}

func NewChannelsHandler(apiProvider *provider.ApiProvider) *ChannelsHandler {
	return &ChannelsHandler{
		apiProvider: apiProvider,
	}
}

func (ch *ChannelsHandler) ChannelsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sortType := request.Params.Arguments["sort"]
	if sortType == "" || sortType == nil {
		sortType = "popularity"
	}

	// Get limit parameter
	limit := 100
	if limitParam, ok := request.Params.Arguments["limit"].(float64); ok {
		limit = int(limitParam)
		if limit > 1000 {
			limit = 1000
		}
		if limit < 1 {
			limit = 1
		}
	}

	types, ok := request.Params.Arguments["channel_types"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("channel_types should be an array")
	}
	channelTypes := []string{}
	for i, v := range types {
		s, ok := v.(string)
		if !ok || s == "" {
			fmt.Printf("element at index %d is not a string\n", i)
			continue
		}
		channelTypes = append(channelTypes, s)
	}

	api, err := ch.apiProvider.Provide()
	if err != nil {
		return nil, err
	}

	if len(types) == 0 {
		channelTypes = PubChanType
	}

	var channels []slack.Channel

	params := &slack.GetConversationsParameters{
		Types:           channelTypes,
		Limit:           100,
		ExcludeArchived: true,
	}
	var total int
	for i := 1; ; i++ {
		var (
			chans   []slack.Channel
			nextcur string
		)

		chans, nextcur, err = api.GetConversationsContext(ctx, params)
		if err != nil {
			return nil, err
		}

		channels = append(channels, chans...)
		total += len(chans)

		// Stop if we've collected enough channels or no more pages
		if len(channels) >= limit || nextcur == "" {
			log.Printf("channels fetch complete, collected %d channels", len(channels))
			break
		}

		params.Cursor = nextcur
	}

	// Trim to limit if we got more than requested
	if len(channels) > limit {
		channels = channels[:limit]
	}

	var channelList []Channel
	for _, channel := range channels {
		channelList = append(channelList, Channel{
			ID:          channel.ID,
			Name:        "#" + channel.Name,
			Topic:       channel.Topic.Value,
			Purpose:     channel.Purpose.Value,
			MemberCount: channel.NumMembers,
		})
	}

	switch sortType {
	case "popularity":
		sort.Slice(channelList, func(i, j int) bool {
			return channelList[i].MemberCount > channelList[j].MemberCount
		})
	default:
		// pass
	}

	csvBytes, err := gocsv.MarshalBytes(&channelList)
	if err != nil {
		return nil, err
	}

	return mcp.NewToolResultText(string(csvBytes)), nil
}
