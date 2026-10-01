//go:build !unix

package report

import "os"

// ownedByCurrentUser has no uid to compare off Unix; there the reports
// directory's protection is the user profile's ACL.
func ownedByCurrentUser(os.FileInfo) bool { return true }
