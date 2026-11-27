// Command fake-acp is a standalone fake ACP agent for the phase 6 demo
// (ADR, "Phase 6 demo and gate"). It is built only by hand:
//
//	go build -o "$T/bin/fake-acp" ./internal/acp/acptest/cmd/fake-acp
//
// Flags:
//
//	--version            print "fake-acp 1.2.3" and exit
//	--mode=ok            answer initialize with ACP v1 (the default)
//	--mode=auth          answer initialize with error -32000 (auth required)
//	--mode=v2            answer with protocolVersion 2
//	--mode=hang          never answer
//	--mode=exit          exit before answering
//	--mode=garbage       write a line that is not JSON-RPC
//	--status=in          a sign-in status command: exit 0 (signed in)
//	--status=out         a sign-in status command: print "Not logged in", exit 1
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	mode := "ok"
	for _, a := range args {
		switch {
		case a == "--version":
			fmt.Println("fake-acp 1.2.3")
			return 0
		case a == "--status=in":
			return 0
		case a == "--status=out":
			fmt.Println("Not logged in")
			return 1
		case strings.HasPrefix(a, "--status="):
			return 2
		case strings.HasPrefix(a, "--mode="):
			mode = strings.TrimPrefix(a, "--mode=")
		}
	}
	var s acptest.Script
	switch mode {
	case "ok":
	case "auth":
		s.InitializeError = &jsonrpc.Error{Code: acp.CodeAuthRequired, Message: "Authentication required"}
	case "v2":
		r := acptest.OK()
		r.ProtocolVersion = 2
		s.Initialize = &r
	case "hang":
		s.Delay = 24 * time.Hour
	case "exit":
		s.ExitBefore = true
	case "garbage":
		s.Garbage = true
	default:
		fmt.Fprintf(os.Stderr, "fake-acp: unknown mode %q\n", mode)
		return 2
	}
	return acptest.Run("fake-acp", s, nil, os.Stdin, os.Stdout, os.Stderr)
}
