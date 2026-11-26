//go:build windows

package watch

import "os"

// ownedByCurrentUser always reports true on Windows: the owner is an ACL
// entry there, and the user profile directories agentflow watches are
// private to the user by default.
func ownedByCurrentUser(os.FileInfo) bool { return true }
