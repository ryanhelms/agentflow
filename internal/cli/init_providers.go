package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/setup/initproviders"
)

// initInspection lets the read-only JSON endpoint run without starting the background updater.
func initInspection(args []string) bool {
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if name == "--detect-providers" || name == "-detect-providers" {
			return true
		}
	}
	return false
}

func providerArgument(answers map[string]string, arg string) error {
	role, id, ok := strings.Cut(arg, "=")
	if !ok || role == "" || id == "" {
		return errors.New("expected --provider ROLE=ID")
	}
	if previous, ok := answers[role]; ok && previous != id {
		return fmt.Errorf("conflicting choices for %s", role)
	}
	answers[role] = id
	return nil
}

func guideProviders(plan *initproviders.Plan, answers map[string]string, in io.Reader, out io.Writer) error {
	if err := plan.ValidateAnswers(answers); err != nil {
		return err
	}
	reader := bufio.NewScanner(in)
	read := func() (string, error) {
		if reader.Scan() {
			return strings.TrimSpace(reader.Text()), nil
		}
		if err := reader.Err(); err != nil {
			return "", err
		}
		return "", errors.New("provider setup cancelled before saving choices")
	}
	fmt.Fprintln(out, "Repository providers")
	for _, r := range plan.Roles {
		label := r.Selected
		if id, ok := answers[r.Role]; ok {
			label = id + " (command line)"
		} else if label == "" {
			label = "choose a provider"
		} else {
			label += " (" + r.Source + ")"
		}
		fmt.Fprintf(out, "  %s: %s\n", r.Role, label)
		for _, e := range r.Evidence {
			fmt.Fprintf(out, "    %s: %s\n", e.Provider, e.Source)
		}
		if r.Selected != "" {
			if _, given := answers[r.Role]; !given {
				answers[r.Role] = r.Selected
			}
		}
	}
	choose := func(r initproviders.Role) error {
		fallback := answers[r.Role]
		if fallback == "" {
			fallback = r.Default
		}
		for {
			fmt.Fprintf(out, "%s (%s) [%s]: ", r.Role, strings.Join(r.Options, ", "), fallback)
			id, err := read()
			if err != nil {
				return err
			}
			if id == "" {
				id = fallback
			}
			if slices.Contains(r.Options, id) {
				answers[r.Role] = id
				return nil
			}
			fmt.Fprintf(out, "Choose a listed provider for %s.\n", r.Role)
		}
	}
	for _, r := range plan.Roles {
		if _, known := answers[r.Role]; !known {
			if err := choose(r); err != nil {
				return err
			}
		}
	}
	for {
		fmt.Fprintln(out, "Provider choices (switching a provider replaces that role's inline settings):")
		for _, r := range plan.Roles {
			fmt.Fprintf(out, "  %s = %s\n", r.Role, answers[r.Role])
		}
		fmt.Fprint(out, "Enter a role to change, or press Enter to use these choices: ")
		role, err := read()
		if err != nil {
			return err
		}
		if role == "" {
			return nil
		}
		index := slices.IndexFunc(plan.Roles, func(r initproviders.Role) bool { return r.Role == role })
		if index < 0 {
			fmt.Fprintln(out, "Enter one of the role names shown above.")
			continue
		}
		if err := choose(plan.Roles[index]); err != nil {
			return err
		}
	}
}
