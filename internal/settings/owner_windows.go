//go:build windows

package settings

import "os"

// ownedByCurrentUser always reports true on Windows: the owner is an ACL
// entry there, and the agentflow home is private to the user by default.
func ownedByCurrentUser(os.FileInfo) bool { return true }
