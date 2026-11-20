// Command devflow links git work to Jira, Confluence and GitHub from any AI client.
package main

import (
	"os"

	"github.com/nscaledev/devflow/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
