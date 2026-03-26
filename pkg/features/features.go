package features

import (
	"context"
)

// Feature represents a semantic Slack operation
type Feature struct {
	Name        string
	Description string
	Schema      interface{}
	Handler     func(context.Context, map[string]interface{}) (*FeatureResult, error)
}

// FeatureResult provides structured responses with guidance
type FeatureResult struct {
	Success     bool        `json:"success"`
	Data        interface{} `json:"data"`
	Message     string      `json:"message"`
	NextActions []string    `json:"nextActions,omitempty"`
	Guidance    string      `json:"guidance,omitempty"`
	ResultCount int         `json:"resultCount,omitempty"`
	Pagination  *Pagination `json:"pagination,omitempty"`
}

// Pagination provides cursor-based pagination info
type Pagination struct {
	Cursor     string `json:"cursor,omitempty"`     // Current cursor
	NextCursor string `json:"nextCursor,omitempty"` // Cursor for next page
	HasMore    bool   `json:"hasMore"`              // More results available
	PageSize   int    `json:"pageSize"`             // Items in this page
	TotalCount int    `json:"totalCount,omitempty"` // Total items (if known)
}

// Registry holds all available features
type Registry struct {
	features map[string]*Feature
}

// NewRegistry creates a new feature registry
func NewRegistry() *Registry {
	return &Registry{
		features: make(map[string]*Feature),
	}
}

// Register adds a feature to the registry
func (r *Registry) Register(feature *Feature) {
	r.features[feature.Name] = feature
}

// Get retrieves a feature by name
func (r *Registry) Get(name string) (*Feature, bool) {
	feature, ok := r.features[name]
	return feature, ok
}

// All returns all registered features
func (r *Registry) All() []*Feature {
	features := make([]*Feature, 0, len(r.features))
	for _, f := range r.features {
		features = append(features, f)
	}
	return features
}
