package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/nscaledev/agentflow/internal/app"
	"github.com/nscaledev/agentflow/internal/configconsole"
	"golang.org/x/term"
)

const configUsage = "agentflow config OP [--input FILE|-]\nOperations: list, read, compose, autonomy, stage_autonomy, preview_personal, preview, readiness, save_personal, publish"

func runConfig(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stdout, configUsage)
		return flag.ErrHelp
	}
	op := args[0]
	f := flag.NewFlagSet("config "+op, flag.ContinueOnError)
	path := f.String("input", "", "JSON request file or - for standard input")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New(configUsage)
	}
	a, err := app.Open(ctx, ".", "cli")
	if err != nil {
		a, err = configconsole.Recovery(ctx, ".", "cli", err)
		if err != nil {
			return err
		}
	}
	if op == "publish" || op == "save_personal" {
		var in configconsole.ApplyRequest
		if err := readConfigRequest(*path, &in); err != nil {
			return err
		}
		if in.Operation != "" && in.Operation != op {
			return errors.New("input operation differs from command")
		}
		in.Operation = op
		return writeResult(configconsole.Apply(ctx, a, in))
	}
	var in configconsole.Query
	if err := readConfigRequest(*path, &in); err != nil {
		return err
	}
	if in.Operation != "" && in.Operation != op {
		return errors.New("input operation differs from command")
	}
	in.Operation = op
	return writeResult(configconsole.Inspect(ctx, a, in))
}

func readConfigRequest(path string, into any) error {
	var input io.Reader = os.Stdin
	if path == "" && term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	if path != "" && path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		input = file
	}
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return err
	}
	if len(data) > limit {
		return errors.New("configuration request exceeds 2 MiB")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 && path == "" {
		return nil
	}
	if len(data) == 0 || data[0] != '{' {
		return errors.New("expected one JSON configuration request object")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return errors.New("invalid JSON configuration request or unknown field")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("expected one JSON configuration request object")
	}
	return nil
}
