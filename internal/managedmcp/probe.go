package managedmcp

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ryanhelms/agentflow/internal/provision"
)

// Apply credentials only to requests to the declared HTTPS endpoint. Disable
// redirects and SDK retries so an endpoint cannot redirect a bearer token.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func (s Status) probe(ctx context.Context, home string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var token string
	if s.AuthEnv != "" {
		token = os.Getenv(s.AuthEnv)
		if token == "" {
			return false, "set " + s.AuthEnv + " in the environment running setup and in the target client"
		}
	}
	var transport mcp.Transport
	if s.Kind == provision.StdioMCP {
		cmd := exec.CommandContext(ctx, s.Command, s.Args...)
		cmd.Dir = home
		cmd.Env = []string{}
		if runtime.GOOS == "windows" {
			cmd.Env = append(cmd.Env, "SystemRoot="+os.Getenv("SystemRoot"))
		}
		if s.AuthEnv != "" {
			cmd.Env = append(cmd.Env, s.AuthEnv+"="+token)
		}
		transport = &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}
	} else {
		transport = &mcp.StreamableClientTransport{
			Endpoint: s.Endpoint, MaxRetries: -1, DisableStandaloneSSE: true,
			HTTPClient: &http.Client{
				Timeout:       10 * time.Second,
				Transport:     bearerTransport{token},
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "agentflow-setup", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, Logger: slog.New(slog.DiscardHandler)})
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return false, "MCP initialization failed. Check the declared transport and authentication reference"
	}
	defer session.Close()
	if _, err = session.ListTools(ctx, nil); err != nil {
		return false, "MCP tools/list failed. Check the companion and read access"
	}
	return true, "MCP handshake and tools/list succeeded"
}
