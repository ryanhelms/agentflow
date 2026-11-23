package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"

	"github.com/nscaledev/devflow/internal/webui"
)

func runUI(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("ui", flag.ContinueOnError)
	port := f.Int("port", 0, "loopback port (default: choose an available port)")
	noOpen := f.Bool("no-open", false, "print the one-time launch URL without opening a browser")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() > 1 || *port < 0 || *port > 65535 {
		return errors.New("usage: devflow ui [--no-open] [--port N] [directory]")
	}
	dir := "."
	if f.NArg() == 1 {
		dir = f.Arg(0)
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	return webui.Serve(ctx, dir, *port, func(url string) {
		fmt.Fprintln(os.Stdout, url)
		if !*noOpen {
			if err := openConsoleBrowser(url); err != nil {
				fmt.Fprintln(os.Stderr, "Open the printed URL in a local browser.")
			}
		}
	})
}

func openConsoleBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
