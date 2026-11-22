// Package twg adapts the Atlassian Teamwork Graph CLI's native Jira and
// Confluence commands using its saved OAuth login.
package twg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nscaledev/devflow/internal/provider"
)

var (
	ErrToolAbsent   = errors.New("twg executable is absent")
	ErrAuthRequired = errors.New("twg login is required")
	ErrAccessDenied = errors.New("twg access denied")
	ErrFailure      = errors.New("twg operation failed")
)

const (
	commandTimeout = 20 * time.Second
	stdoutLimit    = 4 << 20
	stderrLimit    = 64 << 10
)

type output struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (o *output) Write(p []byte) (int, error) {
	n := min(len(p), o.limit-o.buffer.Len())
	o.truncated = o.truncated || n < len(p)
	_, _ = o.buffer.Write(p[:n])
	return len(p), nil
}

type envelope struct {
	Data    json.RawMessage   `json:"data"`
	Error   json.RawMessage   `json:"error"`
	Errors  []json.RawMessage `json:"errors"`
	Success *bool             `json:"success"`
	OK      *bool             `json:"ok"`
	Partial bool              `json:"partial"`
}

func (b binding) read(ctx context.Context, name string, args []string, dest any) error {
	_, err := b.run(ctx, name, args, dest)
	return err
}

// run reports whether the subprocess started, the point after which a mutation
// may have reached Jira even if TWG fails to return an acknowledgement.
func (b binding) run(ctx context.Context, name string, args []string, dest any) (started bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("%s: %w", name, errors.Join(ErrFailure, err))
	}

	cmd := exec.CommandContext(ctx, "twg", append(args, "--site", b.site, "--output", "json", "--output-summary", "none")...)
	cmd.Env = commandEnv()
	cmd.WaitDelay = 250 * time.Millisecond
	stdout, stderr := output{limit: stdoutLimit}, output{limit: stderrLimit}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	started = cmd.Process != nil
	if err := ctx.Err(); err != nil {
		return started, fmt.Errorf("%s: %w", name, errors.Join(ErrFailure, err))
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		return started, fmt.Errorf("%s: %w", name, ErrToolAbsent)
	}
	return started, decodeResult(name, dest, &stdout, &stderr, runErr)
}

func decodeResult(name string, dest any, stdout, stderr *output, runErr error) error {
	var result envelope
	validJSON := !stdout.truncated && json.Unmarshal(stdout.buffer.Bytes(), &result) == nil
	if validJSON && (present(result.Error) || len(result.Errors) > 0) {
		return fmt.Errorf("%s: %w", name, responseError(result))
	}
	if runErr != nil && validJSON && len(result.Data) > 0 && result.Data[0] == '[' {
		// A Jira get can report a failed lookup in its batch row, even when
		// the command also exits nonzero.
		var rows []struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(result.Data, &rows) == nil && len(rows) == 1 && present(rows[0].Error) {
			return fmt.Errorf("%s: %w", name, classify(rows[0].Error))
		}
	}
	if runErr != nil {
		var diagnostic envelope
		if !stderr.truncated && json.Unmarshal(stderr.buffer.Bytes(), &diagnostic) == nil &&
			(present(diagnostic.Error) || len(diagnostic.Errors) > 0) {
			return fmt.Errorf("%s: %w", name, responseError(diagnostic))
		}
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			if exit.ExitCode() == 77 && !stdout.truncated && len(bytes.TrimSpace(stdout.buffer.Bytes())) == 0 {
				return fmt.Errorf("%s: %w", name, ErrAuthRequired)
			}
			return fmt.Errorf("%s: exit %d: %w", name, exit.ExitCode(), ErrFailure)
		}
		return fmt.Errorf("%s: start or wait for executable: %w", name, ErrFailure)
	}
	if !validJSON || !present(result.Data) || result.Partial || failed(result.Success) || failed(result.OK) ||
		json.Unmarshal(result.Data, dest) != nil {
		return fmt.Errorf("%s: invalid or incomplete json result: %w", name, ErrFailure)
	}
	return nil
}

func failed(value *bool) bool {
	return value != nil && !*value
}

func present(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(raw, []byte("null"))
}

func responseError(e envelope) error {
	if present(e.Error) {
		return classify(e.Error)
	}
	if len(e.Errors) == 1 {
		return classify(e.Errors[0])
	}
	return ErrFailure
}

func classify(raw json.RawMessage) error {
	var e struct {
		Code       string          `json:"code"`
		Status     json.RawMessage `json:"status"`
		StatusCode json.RawMessage `json:"statusCode"`
		HTTPStatus json.RawMessage `json:"httpStatus"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ErrFailure
	}
	code := strings.ToUpper(strings.ReplaceAll(e.Code, "-", "_"))
	switch code {
	case "ACCESS_DENIED", "FORBIDDEN", "HTTP_403", "INSUFFICIENT_SCOPE", "PERMISSION_DENIED", "SITE_ACCESS_DENIED":
		return ErrAccessDenied
	}
	for _, status := range []json.RawMessage{e.HTTPStatus, e.StatusCode, e.Status} {
		httpStatus, _ := strconv.Atoi(strings.Trim(string(status), `"`))
		switch httpStatus {
		case 401:
			return ErrAuthRequired
		case 403:
			return ErrAccessDenied
		case 404:
			return provider.ErrNotFound
		}
	}
	switch code {
	case "AUTH_EXPIRED", "AUTH_INVALID", "AUTH_MIGRATION_REQUIRED", "AUTH_REQUIRED", "HTTP_401", "UNAUTHENTICATED":
		return ErrAuthRequired
	case "HTTP_404", "NOT_FOUND", "RESOURCE_NOT_FOUND":
		return provider.ErrNotFound
	default:
		return ErrFailure
	}
}

// Build an allowlist for the saved vendor login, system certificate discovery,
// and platform keychain access. Credential overrides and TWG routing/format
// variables never reach the subprocess.
func commandEnv() []string {
	keys := []string{
		"HOME", "PATH", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS",
		"USERPROFILE", "APPDATA", "LOCALAPPDATA", "SYSTEMROOT", "WINDIR", "TEMP", "TMP",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
	}
	env := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	// TWG treats detached/CI commands as non-interactive.
	return append(env, "CI=true")
}
