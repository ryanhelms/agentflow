// Command agentflow links git work to Jira, Confluence and GitHub from any AI client.
package main

import (
	"os"

	"github.com/ryanhelms/agentflow/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
