package features

// SearchDigestForTest exposes the search digest so tests can forge cursors.
func SearchDigestForTest(query string, channels, people []string) string {
	return searchDigest(query, &searchFilters{channels: channels}, people)
}
