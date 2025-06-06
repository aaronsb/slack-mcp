package features

import (
	"context"
	"fmt"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"log"
)

// DebugInternal is a debugging tool to test internal Slack endpoints
var DebugInternal = &Feature{
	Name:        "debug-internal",
	Description: "Debug tool to test internal Slack API endpoints",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"endpoint": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"counts", "boot", "both"},
				"description": "Which internal endpoint to test",
				"default":     "counts",
			},
		},
	},
	Handler: debugInternalHandler,
}

func debugInternalHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	// Extract parameters
	endpoint := "counts"
	if e, ok := params["endpoint"].(string); ok {
		endpoint = e
	}

	// Get the API provider
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	// Get internal client
	internalClient := apiProvider.ProvideInternalClient()
	if internalClient == nil {
		return &FeatureResult{
			Success: false,
			Message: "Internal client not available",
		}, nil
	}

	result := &FeatureResult{
		Success: true,
		Data:    map[string]interface{}{},
	}

	// Test client.counts endpoint
	if endpoint == "counts" || endpoint == "both" {
		log.Println("Testing /api/client.counts endpoint...")
		counts, err := internalClient.GetClientCounts(ctx)
		if err != nil {
			log.Printf("Error getting client counts: %v", err)
			result.Data.(map[string]interface{})["counts_error"] = err.Error()
		} else {
			// Count totals
			totalChannelUnreads := 0
			totalMentions := 0
			totalDMs := 0
			
			for _, ch := range counts.Channels {
				if ch.HasUnreads {
					totalChannelUnreads++
				}
				totalMentions += ch.MentionCount
			}
			
			for _, im := range counts.IMs {
				if im.HasUnreads && im.MentionCount > 0 {
					totalDMs++
				}
			}
			
			result.Data.(map[string]interface{})["counts"] = map[string]interface{}{
				"ok":                  counts.OK,
				"error":               counts.Error,
				"total_channels":      len(counts.Channels),
				"channels_with_unreads": totalChannelUnreads,
				"total_mentions":      totalMentions,
				"total_dms":           totalDMs,
				"thread_unreads":      counts.Threads.UnreadCount,
				"thread_mentions":     counts.Threads.MentionCount,
				"channel_badges":      counts.ChannelBadges,
				"sample_channels":     getSampleChannels(counts),
			}
		}
	}

	// Test client.boot endpoint
	if endpoint == "boot" || endpoint == "both" {
		log.Println("Testing /api/client.boot endpoint...")
		boot, err := internalClient.GetClientBoot(ctx)
		if err != nil {
			log.Printf("Error getting client boot: %v", err)
			result.Data.(map[string]interface{})["boot_error"] = err.Error()
		} else {
			// Count channels with unreads
			channelsWithUnreads := 0
			totalUnreads := 0
			
			for _, ch := range boot.Channels {
				if ch.UnreadCount > 0 {
					channelsWithUnreads++
					totalUnreads += ch.UnreadCount
				}
			}
			
			result.Data.(map[string]interface{})["boot"] = map[string]interface{}{
				"ok":                    boot.OK,
				"error":                 boot.Error,
				"self_id":               boot.Self.ID,
				"self_name":             boot.Self.Name,
				"team_name":             boot.Team.Name,
				"total_channels":        len(boot.Channels),
				"total_ims":             len(boot.IMs),
				"channels_with_unreads": channelsWithUnreads,
				"total_unreads":         totalUnreads,
			}
		}
	}

	// Build message
	if endpoint == "both" {
		result.Message = "Tested both internal endpoints - see data for results"
	} else {
		result.Message = fmt.Sprintf("Tested /api/client.%s endpoint - see data for results", endpoint)
	}

	return result, nil
}

// Helper to get a sample of channels with unreads
func getSampleChannels(counts *provider.ClientCountsResponse) []map[string]interface{} {
	samples := []map[string]interface{}{}
	count := 0
	
	for _, ch := range counts.Channels {
		if ch.HasUnreads && count < 5 {
			samples = append(samples, map[string]interface{}{
				"id":            ch.ID,
				"mention_count": ch.MentionCount,
				"has_unreads":   ch.HasUnreads,
			})
			count++
		}
	}
	
	return samples
}