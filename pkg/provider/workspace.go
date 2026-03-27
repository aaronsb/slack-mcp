package provider

import (
	"fmt"
	"sync"
)

// WorkspaceManager manages multiple workspace ApiProviders
type WorkspaceManager struct {
	providers        map[string]*ApiProvider
	defaultWorkspace string
	mu               sync.RWMutex
}

// NewWorkspaceManager creates a new workspace manager
func NewWorkspaceManager() *WorkspaceManager {
	return &WorkspaceManager{
		providers: make(map[string]*ApiProvider),
	}
}

// AddWorkspace registers a workspace with the given tokens
func (wm *WorkspaceManager) AddWorkspace(name, xoxcToken, xoxdToken string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	wm.providers[name] = NewWithTokens(xoxcToken, xoxdToken)

	// First workspace added becomes the default
	if wm.defaultWorkspace == "" {
		wm.defaultWorkspace = name
	}
}

// SetDefault sets the default workspace
func (wm *WorkspaceManager) SetDefault(name string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.defaultWorkspace = name
}

// GetProvider returns the provider for the given workspace name
// If name is empty, returns the default workspace provider
func (wm *WorkspaceManager) GetProvider(name string) (*ApiProvider, error) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	if name == "" {
		name = wm.defaultWorkspace
	}

	if name == "" {
		return nil, fmt.Errorf("no workspace configured")
	}

	p, ok := wm.providers[name]
	if !ok {
		return nil, fmt.Errorf("workspace %q not found", name)
	}

	return p, nil
}

// GetDefault returns the default provider (for backward compatibility)
func (wm *WorkspaceManager) GetDefault() (*ApiProvider, error) {
	return wm.GetProvider("")
}

// ListWorkspaces returns all configured workspace names
func (wm *WorkspaceManager) ListWorkspaces() []string {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	names := make([]string, 0, len(wm.providers))
	for name := range wm.providers {
		names = append(names, name)
	}
	return names
}

// DefaultWorkspace returns the default workspace name
func (wm *WorkspaceManager) DefaultWorkspace() string {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	return wm.defaultWorkspace
}

// WorkspaceCount returns the number of configured workspaces
func (wm *WorkspaceManager) WorkspaceCount() int {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	return len(wm.providers)
}
