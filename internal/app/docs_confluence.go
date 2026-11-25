package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/nscaledev/agentflow/internal/atlassian"
	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/policy"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/confluence"
	"github.com/nscaledev/agentflow/internal/provider/native"
	"github.com/nscaledev/agentflow/internal/provider/nativemap"
	"github.com/nscaledev/agentflow/internal/provider/twg"
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
	if err := a.confluenceWriteScope(site, in.Ref.Scope); err != nil {
		return nil, err
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
			if token == "" && prepare.prior.Status == provider.AlreadyApplied {
				return prepare.prior, nil
			}
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
	binding, _ := summary["native_binding"].(native.WriteBinding)
	if p, err := a.gate(policy.Remote, "docs_publish", summary, token, false, nil, binding.ExpiresAt); p != nil || err != nil {
		return p, err
	}
	out, err := apply()
	if summary["backend"] == "twg" {
		err = atlCLIError("confluence", err)
	}
	a.audit(policy.Remote, "docs_publish", summary, err)
	return out, err
}

// Space keys identify targets within a site. A repository space allow-list
// therefore also pins the site where that list was defined.
func (a *App) confluenceWriteScope(site, space string) error {
	sel := a.Cfg.Providers.KnowledgeBase
	repoSpaces, _ := sel.RepositorySettings("publish_spaces").(*config.ConfluenceSettings)
	repoSite, _ := sel.RepositorySettings("site").(*config.ConfluenceSettings)
	if repoSpaces != nil {
		repoSite = repoSpaces
	}
	if repoSite != nil && strings.TrimRight(repoSite.Site, "/") != strings.TrimRight(site, "/") {
		return fmt.Errorf("confluence site differs from the configured repository site: %w", native.ErrDenied)
	}
	current, _ := sel.Settings.(*config.ConfluenceSettings)
	_, configured := sel.Sources["publish_spaces"]
	if (repoSpaces != nil && !slices.Contains(repoSpaces.PublishSpaces, space)) ||
		(current != nil && (configured || current.PublishSpaces != nil) && !slices.Contains(current.PublishSpaces, space)) {
		return fmt.Errorf("space %q is outside knowledge_base.settings.publish_spaces: %w", space, native.ErrDenied)
	}
	if err := sel.CheckTarget("publish_spaces", space); err != nil {
		return fmt.Errorf("confluence space %q: %w", space, errors.Join(native.ErrDenied, err))
	}
	return nil
}

type preparedNativeConfluence struct {
	binding       native.WriteBinding
	spaceID       string
	payloadDigest string
	prior         provider.WriteResult
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
	call := pub.UpdateCall("agentflow-schema-probe")
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
				return atlassian.PageVersion{}, err
			}
			return pub.DecodeVersion(raw, number)
		})
	}
	prior, err := replay(page)
	if errors.Is(err, native.ErrUnavailable) {
		// Missing history on the selected route cannot authorize fallback.
		return zero, errors.New("selected native confluence route cannot read historical versions")
	}
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
			// The flat Rovo update can return without page identity. Verify the
			// persisted successor on the bound connection before reporting success.
			confirmed := false
			if exact.Owner != "agentflow" {
				payload, err := native.Payload(raw)
				if err != nil {
					return provider.WriteResult{}, errors.Join(native.ErrWriteUncertain, err)
				}
				confirmed, err = pub.CheckAcknowledgement(payload, page.SpaceID)
				if err != nil {
					return provider.WriteResult{}, errors.Join(native.ErrWriteUncertain, err)
				}
			}
			if !confirmed {
				current, err := readPage()
				if err != nil || current.SpaceID != page.SpaceID {
					return provider.WriteResult{}, fmt.Errorf("cannot verify native confluence update in the approved space: %w",
						errors.Join(native.ErrWriteUncertain, nativemap.ErrInvalidResult, err))
				}
				status, err := replay(current)
				if err != nil || status.Status != provider.AlreadyApplied ||
					(current.Version.Number == pub.Successor.Number && current.Title != pub.Title) {
					return provider.WriteResult{}, fmt.Errorf("cannot verify native confluence update at the exact successor: %w",
						errors.Join(native.ErrWriteUncertain, nativemap.ErrInvalidResult, err))
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
		binding: binding, spaceID: page.SpaceID, payloadDigest: fmt.Sprintf("%x", sha256.Sum256(payload)), prior: prior, apply: apply,
	}, nil
}
