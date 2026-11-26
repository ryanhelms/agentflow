package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/atlassian"
	"github.com/ryanhelms/agentflow/internal/provider"
)

func runDocs(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: agentflow docs get REF [--json] | agentflow docs search QUERY [--space S] | agentflow docs publish REF --title T --body TEXT --operation-id ID [--expected-revision REV] [--links JSON] [--type TYPE] [--description TEXT] [--tags JSON] [--yes]")
	}
	switch args[0] {
	case "get":
		fs := flag.NewFlagSet("docs get", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "print the document as JSON")
		pos, err := parseInterleaved(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: agentflow docs get REF [--json]")
		}
		ref, err := provider.ParseRef(pos[0])
		if err != nil {
			return err
		}
		a, err := open(ctx)
		if err != nil {
			return err
		}
		doc, err := a.DocsGet(ctx, ref)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(doc)
		}
		printDocument(doc)
		return nil
	case "search":
		fs := flag.NewFlagSet("docs search", flag.ContinueOnError)
		space := fs.String("space", "", "knowledge base scope (e.g. a Confluence space)")
		asJSON := fs.Bool("json", false, "print search results as JSON")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		a, err := open(ctx)
		if err != nil {
			return err
		}
		out, err := a.DocsSearch(ctx, args[1], *space)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(out)
		}
		switch pages := out["pages"].(type) {
		case []atlassian.Page:
			for _, p := range pages {
				fmt.Printf("%s [%s]\n  %s\n  %s\n", p.Title, p.Space, p.URL, p.Excerpt)
			}
		case []provider.Document:
			for _, p := range pages {
				printDocument(p)
			}
		default:
			return errors.New("unknown knowledge search result")
		}
		return nil
	case "publish":
		in := app.DocsPublishInput{}
		fs := flag.NewFlagSet("docs publish", flag.ContinueOnError)
		fs.StringVar(&in.Title, "title", "", "document title")
		fs.StringVar(&in.Body, "body", "", "Markdown document body")
		fs.StringVar(&in.ExpectedRevision, "expected-revision", "", "revision returned by search (required for updates)")
		fs.StringVar(&in.OperationID, "operation-id", "", "stable ID to reuse on retry")
		fs.StringVar(&in.Type, "type", "", "OKF concept type (defaults to Document on create)")
		fs.StringVar(&in.Description, "description", "", "OKF concept description")
		fs.Func("tags", "OKF concept tags as a JSON array of strings", func(value string) error {
			var tags []*string
			if err := json.Unmarshal([]byte(value), &tags); err != nil {
				return fmt.Errorf("invalid --tags: %w", err)
			}
			if tags == nil {
				return errors.New("--tags requires a JSON array")
			}
			in.Tags = make([]string, len(tags))
			for i, tag := range tags {
				if tag == nil {
					return errors.New("--tags entries must be strings")
				}
				in.Tags[i] = *tag
			}
			return nil
		})
		links := fs.String("links", "", "document links as a JSON array")
		yes := fs.Bool("yes", false, "accept the confirmation prompt")
		flags := args[2:]
		if args[1] == "--help" || args[1] == "-h" {
			flags = args[1:]
		}
		if err := fs.Parse(flags); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected positional arguments after the document reference")
		}
		bodyProvided := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "body" {
				bodyProvided = true
			}
		})
		if !bodyProvided {
			return errors.New("--body is required")
		}
		ref, err := provider.ParseRef(args[1])
		if err != nil {
			return err
		}
		in.Ref = ref
		if *links != "" {
			if err := json.Unmarshal([]byte(*links), &in.Links); err != nil {
				return fmt.Errorf("invalid --links: %w", err)
			}
		}
		a, err := open(ctx)
		if err != nil {
			return err
		}
		return confirmLoop(*yes, func(token string) (any, error) { return a.DocsPublish(app.WithConfluenceCLI(ctx), in, token) })
	}
	return fmt.Errorf("unknown docs command %q", args[0])
}

func printDocument(p provider.Document) {
	fmt.Printf("%s [%s]\n  revision %s\n", p.Title, p.Ref, p.Revision)
	if p.URL != "" {
		fmt.Printf("  %s\n", p.URL)
	}
	fmt.Printf("  %s\n", p.Body)
}
