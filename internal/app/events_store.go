package app

import "github.com/ryanhelms/agentflow/internal/bus/store"

// OpenEventStore opens the Message Store of the repository whose common git
// dir is commonDir, in role. A reader gets an error wrapping fs.ErrNotExist
// when the repository has no store yet. Tests replace it.
var OpenEventStore = store.Open
