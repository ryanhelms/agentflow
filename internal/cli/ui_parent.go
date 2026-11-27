package cli

import (
	"errors"
	"os"
	"path/filepath"
)

// `agentflow ui --serve-child` is the console's private startup plumbing: it
// writes the one-time sign-in link to its standard output for the agentflow
// process that started it. So it runs only when its parent process is this
// same agentflow binary (ADR, phase 7 integration amendment on the console
// link). An agent that runs it from a shell has a shell as its parent and
// is refused before anything starts or a link is minted. Where the parent
// cannot be read (Windows, other systems) this check passes: that gap is
// recorded in the ADR.

var errServeChildParent = errors.New("agentflow ui --serve-child is started only by agentflow itself: run `agentflow ui` in your own terminal")

// uiParentIsAgentflow reports whether the parent process runs this binary.
// Tests replace it.
var uiParentIsAgentflow = func() bool {
	parent, ok := parentExecutable(os.Getppid())
	if !ok {
		return parentUnknownAllowed
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	return sameFile(parent, self)
}

func sameFile(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return false
	}
	if ra == rb {
		return true
	}
	ia, err1 := os.Stat(ra)
	ib, err2 := os.Stat(rb)
	return err1 == nil && err2 == nil && os.SameFile(ia, ib)
}
