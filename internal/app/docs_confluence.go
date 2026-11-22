package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/nscaledev/devflow/internal/config"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/confluence"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
	"github.com/nscaledev/devflow/internal/provider/twg"
)

type confluenceCLIKey struct{}

// WithConfluenceCLI enables the saved vendor CLI for interactive publication
// when the authenticated MCP capability is unavailable before any dispatch.
func WithConfluenceCLI(ctx context.Context) context.Context {
	if os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true" ||
		os.Getenv("CONFLUENCE_USER") != "" || os.Getenv("CONFLUENCE_TOKEN") != "" ||
		(os.Getenv("JIRA_USER") != "" && os.Getenv("JIRA_TOKEN") != "") {
		return ctx
	}
	return context.WithValue(ctx, confluenceCLIKey{}, true)
}

func (a *App) publishConfluence(ctx context.Context, in DocsPublishInput, summary map[string]any, token string) (any, error) {
	if _, err := confluence.ReplacementVersion(in.Ref, in.ExpectedRevision, in.Links); err != nil {
		return nil, err
	}
	site, err := a.confluenceSite()
	if err != nil {
		return nil, err
	}
	if repo, ok := a.Cfg.Providers.KnowledgeBase.RepositorySettings("site").(*config.ConfluenceSettings); ok &&
		strings.TrimRight(repo.Site, "/") != strings.TrimRight(site, "/") {
		return nil, fmt.Errorf("confluence site differs from the configured repository site: %w", native.ErrDenied)
	}
	summary["site"] = site
	draft, opts := in.replacement()
	apply := func() (provider.WriteResult, error) {
		k, err := a.confluenceKnowledge(site)
		if err != nil {
			return provider.WriteResult{}, err
		}
		return in.publish(ctx, k)
	}
	summary["backend"] = "rest"
	// Existing automation accepts HTTP fixtures and sites with a path. Vendor
	// interactive transports require an HTTPS site origin.
	u, _ := url.Parse(site) // confluenceSite already validated the URL
	if u.Scheme == "https" && (u.EscapedPath() == "" || u.EscapedPath() == "/") {
		prepare, err := prepareNativeConfluence(ctx, site, in.Ref, draft, opts)
		switch {
		case err == nil:
			summary["backend"], summary["native_binding"] = "mcp", prepare.binding
			summary["space_id"], summary["payload_sha256"] = prepare.spaceID, prepare.payloadDigest
			apply = prepare.apply
		case !errors.Is(err, native.ErrUnavailable):
			summary["backend"] = "mcp"
			a.audit(policy.Remote, "docs_publish", summary, err)
			return nil, err
		default:
			if enabled, _ := ctx.Value(confluenceCLIKey{}).(bool); enabled {
				cli, err := twg.NewConfluence("confluence", site, nil)
				if err != nil {
					return nil, err
				}
				publish, err := cli.PreparePublish(ctx, in.Ref, draft, opts)
				switch {
				case err == nil:
					summary["backend"], summary["twg_intent"] = "twg", publish.Intent()
					apply = func() (provider.WriteResult, error) { return publish.Apply(ctx) }
				case !errors.Is(err, twg.ErrToolAbsent):
					err = atlCLIError("confluence", err)
					summary["backend"] = "twg"
					a.audit(policy.Remote, "docs_publish", summary, err)
					return nil, err
				}
			}
		}
	}
	if p, err := a.gate(policy.Remote, "docs_publish", summary, token, false, nil); p != nil || err != nil {
		return p, err
	}
	out, err := apply()
	if summary["backend"] == "twg" {
		err = atlCLIError("confluence", err)
	}
	a.audit(policy.Remote, "docs_publish", summary, err)
	return out, err
}

type preparedNativeConfluence struct {
	binding       native.WriteBinding
	spaceID       string
	payloadDigest string
	apply         func() (provider.WriteResult, error)
}

func prepareNativeConfluence(ctx context.Context, site string, ref provider.Ref, draft provider.DocumentDraft, opts provider.WriteOptions) (preparedNativeConfluence, error) {
	var zero preparedNativeConfluence
	pub, err := nativemap.PrepareConfluencePublication(site, ref, draft, opts)
	if err != nil {
		return zero, err
	}
	// Acquire the authenticated route with the complete update shape before
	// looking up the real snapshot. This probe can never be dispatched.
	call := pub.UpdateCall("devflow-schema-probe")
	binding, err := native.PrepareWrite(ctx, call)
	if err != nil {
		return zero, err
	}
	read := func(call native.Call) (json.RawMessage, error) {
		raw, err := native.ReadBound(ctx, binding, call)
		if err != nil {
			return nil, err
		}
		return native.Payload(raw)
	}
	readPage := func() (nativemap.ConfluenceSnapshot, error) {
		raw, err := read(pub.PageCall())
		if err != nil {
			return nativemap.ConfluenceSnapshot{}, err
		}
		return pub.DecodeSnapshot(raw)
	}
	page, err := readPage()
	if err != nil {
		if errors.Is(err, native.ErrUnavailable) {
			// The selected write route must also support its trusted preflight.
			return zero, errors.New("selected native confluence route cannot read the published page")
		}
		return zero, err
	}
	replay := func(page nativemap.ConfluenceSnapshot) (provider.WriteResult, error) {
		return pub.Replay(page.Version, page.HasMessage, func(number int64) (atlassian.PageVersion, error) {
			raw, err := read(pub.VersionCall(number))
			if err != nil {
				if errors.Is(err, native.ErrUnavailable) {
					return atlassian.PageVersion{}, errors.New("selected native confluence route cannot read historical versions")
				}
				return atlassian.PageVersion{}, err
			}
			return pub.DecodeVersion(raw, number)
		})
	}
	prior, err := replay(page)
	if err != nil {
		return zero, err
	}
	apply := func() (provider.WriteResult, error) { return prior, nil }
	if prior.Status == "" {
		if page.Token == "" {
			return zero, errors.New("confluence response omitted the snapshot token for the expected revision")
		}
		call = pub.UpdateCall(page.Token)
		exact, err := native.RebindWrite(ctx, binding, call)
		if errors.Is(err, native.ErrUnavailable) {
			return zero, errors.New("selected native confluence route cannot prepare the verified update")
		}
		if err != nil {
			return zero, err
		}
		if exact.Connection != binding.Connection || exact.Provider != binding.Provider || exact.Server != binding.Server ||
			exact.Tool != binding.Tool || exact.Origin != binding.Origin {
			return zero, errors.New("native confluence route changed during publication preparation")
		}
		binding = exact
		apply = func() (provider.WriteResult, error) {
			raw, err := native.Write(ctx, exact, call)
			if errors.Is(err, native.ErrUnavailable) {
				return provider.WriteResult{}, native.ErrWriteUncertain
			}
			if err != nil {
				return provider.WriteResult{}, err
			}
			payload, err := native.Payload(raw)
			if err != nil {
				return provider.WriteResult{}, err
			}
			confirmed, err := pub.CheckAcknowledgement(payload, page.SpaceID)
			if err != nil {
				return provider.WriteResult{}, err
			}
			if !confirmed {
				current, err := readPage()
				if errors.Is(err, native.ErrUnavailable) {
					err = native.ErrWriteUncertain
				}
				if err != nil || current.SpaceID != page.SpaceID {
					return provider.WriteResult{}, fmt.Errorf("cannot verify native confluence update in the approved space: %w",
						errors.Join(nativemap.ErrInvalidResult, err))
				}
				status, err := replay(current)
				if err != nil || status.Status != provider.AlreadyApplied ||
					(current.Version.Number == pub.Successor.Number && current.Title != pub.Title) {
					return provider.WriteResult{}, fmt.Errorf("cannot verify native confluence update at the exact successor: %w",
						errors.Join(nativemap.ErrInvalidResult, err))
				}
			}
			return pub.Result(provider.Applied), nil
		}
	}
	payload, err := json.Marshal(call)
	if err != nil {
		return zero, err
	}
	return preparedNativeConfluence{
		binding: binding, spaceID: page.SpaceID, payloadDigest: fmt.Sprintf("%x", sha256.Sum256(payload)), apply: apply,
	}, nil
}
