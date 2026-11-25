package gh

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type cliBackend struct {
	command string
	dir     string
	host    string
}

// NewCLI uses the login stored by the GitHub CLI for host. Host is a bare
// GitHub hostname. An empty host selects github.com, and an empty command
// selects gh on PATH.
func NewCLI(command, dir, host string) *Client {
	if command == "" {
		command = "gh"
	}
	host = strings.ToLower(host)
	if host == "" || host == "api.github.com" {
		host = "github.com"
	}
	return &Client{cli: &cliBackend{command: command, dir: dir, host: host}}
}

var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)

// gh expands these spellings using the current repository. Preserve them when
// they are part of a literal API path or query.
var escapePlaceholders = strings.NewReplacer(
	"{", "%7B", "}", "%7D",
	":owner", "%3Aowner", ":repo", "%3Arepo", ":branch", "%3Abranch",
)

func cliEndpoint(path string) (string, error) {
	// gh treats any argument containing "://" as an absolute URL.
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		strings.Contains(path, "://") || strings.ContainsAny(path, "\\#") {
		return "", errors.New("github cli requires a relative api path beginning with /")
	}
	u, err := url.ParseRequestURI(path)
	if err != nil {
		return "", errors.New("github cli received an invalid api path")
	}
	return escapePlaceholders.Replace(strings.TrimPrefix(u.RequestURI(), "/")), nil
}

func (c *cliBackend) do(ctx context.Context, method, path string, payload []byte, out any, headers http.Header) (http.Header, error) {
	// gh special-cases github.localhost to use plain HTTP.
	if len(c.host) > 253 || !hostnamePattern.MatchString(c.host) || c.host == "github.localhost" {
		return nil, errors.New("github cli requires a bare github hostname served over https")
	}
	endpoint, err := cliEndpoint(path)
	if err != nil {
		return nil, err
	}
	if method == "" {
		method = http.MethodGet
	}
	args := []string{"api", "--hostname", c.host, "--method", method, "--include"}
	if payload != nil {
		headers.Set("Content-Type", "application/json")
		args = append(args, "--input", "-")
	}
	for name, values := range headers {
		switch name {
		case "Authorization", "Proxy-Authorization", "Cookie", "Host":
			return nil, errors.New("github cli does not accept authentication or host headers")
		}
		for _, value := range values {
			args = append(args, "--header", name+": "+value)
		}
	}
	args = append(args, "--", endpoint)

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.command, args...)
	cmd.Dir = c.dir
	cmd.Env = cliEnv(cmd.Environ())
	cmd.WaitDelay = time.Second
	if payload != nil {
		cmd.Stdin = bytes.NewReader(payload)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// Discard stderr, which can contain URLs or other sensitive diagnostics.
	if err := cmd.Start(); err != nil {
		if cliCommandMissing(cmd, err) {
			return nil, fmt.Errorf("%w: %w", ErrCLIUnavailable, err)
		}
		return nil, fmt.Errorf("start github cli: %w", err)
	}
	runErr := cmd.Wait()

	status, responseHeaders, data, parseErr := readCLIResponse(&stdout)
	// gh reports HTTP failures (including 304) with a nonzero process exit.
	if parseErr == nil && status >= 300 {
		err := decodeResponse(method, path, status, responseHeaders, data, out)
		if apiErr, ok := errors.AsType[*APIError](err); ok && (status == http.StatusRequestTimeout || status >= 500) {
			apiErr.outcomeUnknown = mayWrite(method)
		}
		return responseHeaders, err
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return responseHeaders, cliOutcomeError(method, fmt.Errorf("github cli request: %w", ctx.Err()))
		}
		exitErr, _ := errors.AsType[*exec.ExitError](runErr)
		if exitErr != nil && exitErr.ExitCode() == 4 && parseErr != nil {
			return nil, fmt.Errorf("%w: run gh auth login --hostname %s", ErrAuthentication, c.host)
		}
		// gh may exit 1 for GraphQL errors even when it printed a JSON
		// response with HTTP 200. GraphQL handles that envelope itself.
		if parseErr != nil || exitErr == nil || exitErr.ExitCode() != 1 ||
			endpoint != "graphql" || !hasGraphQLErrors(data) {
			return responseHeaders, cliOutcomeError(method, fmt.Errorf("github cli request failed: %w", runErr))
		}
	}
	if parseErr != nil {
		return nil, cliOutcomeError(method, parseErr)
	}
	err = decodeResponse(method, path, status, responseHeaders, data, out)
	return responseHeaders, cliOutcomeError(method, err)
}

func cliEnv(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
			"GH_HOST", "GH_REPO", "GH_FORCE_TTY", "CLICOLOR_FORCE", "GH_DEBUG", "DEBUG":
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "GH_PROMPT_DISABLED=1", "NO_COLOR=1", "GH_PAGER=cat",
		"GH_NO_UPDATE_NOTIFIER=1", "GH_TELEMETRY=false")
}

func cliCommandMissing(cmd *exec.Cmd, err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	path := cmd.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(cmd.Dir, path)
	}
	_, err = os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

func readCLIResponse(output io.Reader) (int, http.Header, []byte, error) {
	reader := bufio.NewReader(output)
	lines := textproto.NewReader(reader)
	line, err := lines.ReadLine()
	proto, status, _ := strings.Cut(line, " ")
	code, _, _ := strings.Cut(status, " ")
	_, _, httpVersion := http.ParseHTTPVersion(proto)
	statusCode, codeErr := strconv.Atoi(code)
	if err != nil || !httpVersion || len(code) != 3 || codeErr != nil || statusCode < 200 || statusCode > 599 {
		return 0, nil, nil, errors.New("github cli response is missing an http status")
	}
	headers, err := lines.ReadMIMEHeader()
	if err != nil {
		return 0, nil, nil, errors.New("github cli response has invalid http headers")
	}
	// --include prints the decoded body, irrespective of the wire framing
	// described by Content-Length or Transfer-Encoding in the headers.
	data, err := io.ReadAll(reader)
	return statusCode, http.Header(headers), data, err
}

func hasGraphQLErrors(data []byte) bool {
	var resp graphQLResponse
	return json.Unmarshal(data, &resp) == nil && len(resp.Errors) > 0
}
