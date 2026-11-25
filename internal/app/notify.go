package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/notifications"
)

type NotifyInput struct {
	Signal      string          `json:"signal"`
	OperationID string          `json:"operation_id"`
	Title       string          `json:"title,omitempty"`
	Body        string          `json:"body,omitempty"`
	Links       []provider.Link `json:"links,omitempty"`
	DryRun      bool            `json:"-"`
	Automation  bool            `json:"-"` // explicit headless CLI invocation
}

type NotificationStatus string

const (
	NotificationPlanned          NotificationStatus = "planned"
	NotificationSkipped          NotificationStatus = "skipped"
	NotificationDelivered        NotificationStatus = "delivered"
	NotificationAlreadyDelivered NotificationStatus = "already_delivered"
	NotificationRejected         NotificationStatus = "rejected"
	NotificationUnconfirmed      NotificationStatus = "unconfirmed"
	NotificationConflict         NotificationStatus = "conflict"
	NotificationReceiptError     NotificationStatus = "receipt_error"
	NotificationFailed           NotificationStatus = "failed"
)

type NotificationOutcome struct {
	Destination string               `json:"destination"`
	Provider    string               `json:"provider"`
	Status      NotificationStatus   `json:"status"`
	WorkspaceID string               `json:"workspace_id,omitempty"`
	ChannelID   string               `json:"channel_id,omitempty"`
	ReceiptID   string               `json:"receipt_id,omitempty"`
	WriteStatus provider.WriteStatus `json:"write_status,omitempty"`
	URL         string               `json:"url,omitempty"`
	Detail      string               `json:"detail,omitempty"`
}

type NotificationResult struct {
	Signal      string                `json:"signal"`
	OperationID string                `json:"operation_id"`
	DryRun      bool                  `json:"dry_run,omitempty"`
	Reason      string                `json:"reason,omitempty"`
	Deliveries  []NotificationOutcome `json:"deliveries"`
}

func (r NotificationResult) HasFailures() bool {
	for _, outcome := range r.Deliveries {
		switch outcome.Status {
		case NotificationPlanned, NotificationSkipped, NotificationDelivered, NotificationAlreadyDelivered:
		default:
			return true
		}
	}
	return false
}

// Notify dispatches an explicitly submitted signal using only the notification
// role. Partial failures are reported alongside successes in the result.
func (a *App) Notify(ctx context.Context, input NotifyInput, token string) (NotificationResult, *Pending, error) {
	return a.notify(ctx, input, token, nil)
}

func (a *App) notify(ctx context.Context, input NotifyInput, token string, transport http.RoundTripper) (NotificationResult, *Pending, error) {
	result := NotificationResult{
		Signal: input.Signal, OperationID: input.OperationID, DryRun: input.DryRun, Deliveries: []NotificationOutcome{},
	}
	if !config.ValidNotificationName(input.Signal) {
		return result, nil, errors.New("notification signal requires a name containing only letters, digits, dots, underscores or hyphens")
	}
	if strings.TrimSpace(input.OperationID) == "" || strings.ContainsFunc(input.OperationID, unicode.IsControl) {
		return result, nil, errors.New("notification requires a stable operation ID without control characters")
	}
	if strings.TrimSpace(input.Title) == "" && strings.TrimSpace(input.Body) == "" && len(input.Links) == 0 {
		return result, nil, errors.New("notification requires a title, body or link")
	}
	selection := a.Cfg.Providers.Notifications
	targets := notificationTargets(selection, input.Signal)
	if err := checkNotificationScope(selection, input.Signal, targets); err != nil {
		return result, nil, err
	}
	if len(targets) == 0 {
		result.Reason = "notifications disabled or no destinations routed for this signal"
		return result, nil, nil
	}
	store := notifications.FileStore{Dir: filepath.Join(a.Home, "notification-receipts")}
	var id string
	if !input.DryRun {
		var err error
		id, err = a.notificationOperationID(ctx, input)
		if err != nil {
			return result, nil, err
		}
	}
	automation := input.Automation || (a.Client == "cli" && os.Getenv("GITHUB_ACTIONS") == "true")
	var ready []notificationAttempt
	var slackPlans []nativeSlackPlan
	for _, target := range targets {
		outcome := NotificationOutcome{Destination: target.name, Provider: target.Provider, Status: NotificationPlanned}
		if target.Native != nil {
			outcome.WorkspaceID, outcome.ChannelID = target.Native.WorkspaceID, target.Native.ChannelID
		}
		if !input.DryRun {
			notification := provider.Notification{Destination: target.name, Title: input.Title, Body: input.Body, Links: input.Links}
			deliver, plan, status, err := target.prepare(ctx, notification, id, store, transport, automation)
			if err == nil {
				ready = append(ready, notificationAttempt{deliver: deliver, index: len(result.Deliveries)})
				if plan != nil {
					slackPlans = append(slackPlans, *plan)
				}
			} else {
				outcome.Status, outcome.Detail = status, err.Error()
			}
		}
		result.Deliveries = append(result.Deliveries, outcome)
	}
	if len(ready) == 0 {
		return result, nil, nil
	}
	confirmation := struct {
		Input      NotifyInput           `json:"notification"`
		Deliveries []NotificationOutcome `json:"deliveries"`
		Slack      []nativeSlackPlan     `json:"native_slack,omitempty"`
	}{input, slices.Clone(result.Deliveries), slackPlans}
	if pending, err := a.gate(policy.Remote, "notify", confirmation, token, false, nil, slackApprovalExpiry(slackPlans)); pending != nil || err != nil {
		return result, pending, err
	}
	for _, attempt := range ready {
		outcome := &result.Deliveries[attempt.index]
		receipt, err := attempt.deliver(ctx)
		setNotificationOutcome(outcome, receipt, err)
		a.Gate.Audit(policy.Remote, "notify", confirmation, string(outcome.Status))
	}
	return result, nil, nil
}

type notificationTarget struct {
	config.NotificationDestination
	name     string
	optional bool
}

func notificationTargets(selection config.Selection, signal string) []notificationTarget {
	switch settings := selection.Settings.(type) {
	case *config.SlackSettings:
		target := singleNotificationTarget("slack", "SLACK_WEBHOOK_URL", settings.Destination, settings.CredentialRef)
		target.Native = settings.Native
		target.optional = settings.CredentialRef == nil || *settings.CredentialRef == ""
		return []notificationTarget{target}
	case *config.WebhookSettings:
		target := singleNotificationTarget("webhook", "WEBHOOK_URL", settings.Destination, settings.CredentialRef)
		target.HeaderRefs, target.RejectedStatuses = settings.HeaderRefs, settings.RejectedStatuses
		return []notificationTarget{target}
	case *config.NotificationRoutes:
		names := settings.Routes[signal]
		if settings.Routes == nil {
			names = slices.Sorted(maps.Keys(settings.Destinations))
		}
		targets := make([]notificationTarget, 0, len(names))
		seen := map[string]bool{}
		for _, name := range names {
			if !seen[name] {
				targets = append(targets, notificationTarget{name: name, NotificationDestination: settings.Destinations[name]})
				seen[name] = true
			}
		}
		return targets
	default:
		return nil
	}
}

// Resolve the trusted route for the same signal before preparing any of a
// batch's deliveries. A user may narrow a route or supply a missing default.
func checkNotificationScope(selection config.Selection, signal string, targets []notificationTarget) error {
	repo := selection.RepositorySettings("")
	switch s := repo.(type) {
	case *config.SlackSettings:
		for _, dst := range targets {
			if selection.RepositorySettings("native") != nil && !withinSlackScope(dst.Native, s.Native) ||
				selection.RepositorySettings("credential_ref") != nil && s.CredentialRef != nil && *s.CredentialRef != "" && (dst.Native != nil || dst.CredentialRef != *s.CredentialRef) {
				return fmt.Errorf("slack delivery differs from the repository destination: %w", native.ErrDenied)
			}
		}
	case *config.NotificationRoutes:
		allowed := notificationTargets(config.Selection{Settings: s}, signal)
		for _, dst := range targets {
			if !slices.ContainsFunc(allowed, func(trusted notificationTarget) bool {
				return dst.name == trusted.name && (trusted.Provider == "" || trusted.Provider == dst.Provider) &&
					(trusted.CredentialRef == "" || (dst.Native == nil && trusted.CredentialRef == dst.CredentialRef)) &&
					(trusted.Native == nil || withinSlackScope(dst.Native, trusted.Native))
			}) {
				return fmt.Errorf("notification target %q is outside the repository route for %q: %w", dst.name, signal, native.ErrDenied)
			}
		}
	}
	return nil
}

func withinSlackScope(dst, trusted *config.SlackNativeSettings) bool {
	if dst == nil || trusted == nil {
		return dst == trusted
	}
	return (trusted.WorkspaceID == "" || dst.WorkspaceID == trusted.WorkspaceID) &&
		(trusted.ChannelID == "" || dst.ChannelID == trusted.ChannelID) &&
		(trusted.ActorID == "" || dst.ActorID == trusted.ActorID) &&
		(trusted.Server == "" || dst.Server == trusted.Server)
}

func singleNotificationTarget(kind, env string, dest, credential *string) notificationTarget {
	name := kind
	if dest != nil && *dest != "" {
		name = *dest
	}
	if credential != nil && *credential != "" {
		env = *credential
	}
	return notificationTarget{name: name, NotificationDestination: config.NotificationDestination{Provider: kind, CredentialRef: env}}
}

func (target notificationTarget) prepare(ctx context.Context, notification provider.Notification, id string,
	store notifications.FileStore, transport http.RoundTripper, automation bool,
) (notificationDelivery, *nativeSlackPlan, NotificationStatus, error) {
	if target.Native != nil {
		deliver, plan, err := prepareNativeSlack(ctx, *target.Native, notification, id, store)
		return deliver, plan, NotificationFailed, err
	}
	if target.Provider == "slack" && !automation {
		return nil, nil, NotificationFailed, errors.New("configure a native slack workspace and channel for interactive delivery; incoming slack webhooks require automation")
	}
	router, status, err := target.router(store, transport)
	if err != nil {
		return nil, nil, status, err
	}
	return func(ctx context.Context) (provider.DeliveryReceipt, error) {
		return router.Deliver(ctx, notification, provider.WriteOptions{OperationID: id})
	}, nil, NotificationFailed, nil
}

func (target notificationTarget) router(store notifications.FileStore, transport http.RoundTripper) (*notifications.Router, NotificationStatus, error) {
	endpoint := os.Getenv(target.CredentialRef)
	if endpoint == "" {
		status := NotificationFailed
		if target.optional {
			status = NotificationSkipped
		}
		return nil, status, fmt.Errorf("credential environment variable %s is unset", target.CredentialRef)
	}
	headers := http.Header{}
	for _, name := range slices.Sorted(maps.Keys(target.HeaderRefs)) {
		ref := target.HeaderRefs[name]
		value := os.Getenv(ref)
		if value == "" {
			return nil, NotificationFailed, fmt.Errorf("credential environment variable %s is unset", ref)
		}
		headers.Add(name, value)
	}
	router, err := notifications.NewRouter(map[string]notifications.DestinationConfig{target.name: {
		Kind: notifications.ReceiverKind(target.Provider), URL: endpoint, Headers: headers, RejectedStatuses: target.RejectedStatuses,
	}}, store, transport)
	return router, NotificationFailed, err
}

func (a *App) notificationOperationID(ctx context.Context, input NotifyInput) (string, error) {
	origin, _ := a.Repo.Git(ctx, "config", "--get", "remote.origin.url")
	remote, err := gitx.ParseRemoteURL(origin)
	fields := []string{"devflow-notify-v1", input.Signal, input.OperationID}
	if err == nil {
		fields = append(fields, "remote", remote.Host, remote.Port, remote.Path)
	} else {
		dir, err := a.Repo.Git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return "", err
		}
		fields = append(fields, "local", strings.TrimSpace(dir))
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(fields, "\x00")))), nil
}

type notificationAttempt struct {
	deliver notificationDelivery
	index   int
}

type notificationDelivery func(context.Context) (provider.DeliveryReceipt, error)

func setNotificationOutcome(outcome *NotificationOutcome, receipt provider.DeliveryReceipt, err error) {
	outcome.ReceiptID, outcome.WriteStatus, outcome.URL = receipt.ID, receipt.Status, receipt.URL
	switch {
	case err == nil && receipt.Status == provider.AlreadyApplied:
		outcome.Status = NotificationAlreadyDelivered
	case err == nil:
		outcome.Status = NotificationDelivered
	case errors.Is(err, notifications.ErrReceiptStore):
		outcome.Status = NotificationReceiptError
	case errors.Is(err, notifications.ErrUnconfirmed):
		outcome.Status = NotificationUnconfirmed
	case errors.Is(err, notifications.ErrRejected):
		outcome.Status = NotificationRejected
	case errors.Is(err, provider.ErrConflict):
		outcome.Status = NotificationConflict
	default:
		outcome.Status = NotificationFailed
	}
	if err != nil {
		outcome.Detail = err.Error()
	}
}
