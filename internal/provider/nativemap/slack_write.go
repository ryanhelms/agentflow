package nativemap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/notifications"
)

var slackWorkspace = regexp.MustCompile(`^T[A-Z0-9]{8,31}$`)

const (
	slackMessageLimit = 4000
	slackLinkLimit    = 20
	receiptTimeout    = 15 * time.Second
)

// SlackDestination is the trusted routing configuration for a named
// conversation. An instance selects a session bound to WorkspaceID.
type SlackDestination struct {
	Name        string
	Instance    string
	WorkspaceID string
	ChannelID   string
}

// SlackIntent is the exact target and payload to be bound by the app's
// confirmation and audit.
type SlackIntent struct {
	Destination SlackDestination
	OperationID string
	Message     string
}

type SlackNotification struct {
	intent SlackIntent
	call   Call
}

func (n SlackNotification) Intent() SlackIntent { return n.intent }

// PrepareSlackNotification maps one top-level send. The destination is
// resolved by the caller from trusted configuration.
func PrepareSlackNotification(dest SlackDestination, notification provider.Notification, opts provider.WriteOptions) (SlackNotification, error) {
	if opts.ExpectedRevision != "" || opts.CheckTask != nil {
		return SlackNotification{}, provider.Unsupported(dest.Instance, "slack notification with a task or revision precondition")
	}
	if !validName(dest.Name) || !validName(dest.Instance) || !slackWorkspace.MatchString(dest.WorkspaceID) ||
		!slackChannel.MatchString(dest.ChannelID) || dest.Name != notification.Destination {
		return SlackNotification{}, fmt.Errorf("slack notification requires a matching configured workspace and conversation: %w", provider.ErrNotFound)
	}
	if !validName(opts.OperationID) {
		return SlackNotification{}, errors.New("slack notification requires a stable operation id")
	}
	message, err := slackNotificationMessage(notification)
	if err != nil {
		return SlackNotification{}, err
	}
	input := struct {
		ChannelID string `json:"channel_id"`
		Message   string `json:"message"`
	}{dest.ChannelID, message}
	call, err := newCall(Slack, DeliverToSlack, dest.Instance, input)
	if err != nil {
		return SlackNotification{}, err
	}
	return SlackNotification{intent: SlackIntent{dest, opts.OperationID, message}, call: call}, nil
}

var markdownLabel = strings.NewReplacer(
	"\\", "\\\\", "[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>", "\r", " ", "\n", " ",
)

// The hosted message tool accepts standard Markdown.
func slackNotificationMessage(n provider.Notification) (string, error) {
	if len(n.Title)+len(n.Body) > slackMessageLimit || len(n.Links) > slackLinkLimit {
		return "", errors.New("slack notification exceeds the message or link limit")
	}
	parts := []string{n.Title, n.Body}
	for _, link := range n.Links {
		if len(link.Title)+len(link.URL) > slackMessageLimit {
			return "", errors.New("slack notification link exceeds the message limit")
		}
		label := strings.TrimSpace(link.Title)
		if link.Ref != nil {
			if err := link.Ref.Validate(); err != nil {
				return "", err
			}
			if label == "" {
				if len(link.Ref.Instance)+len(link.Ref.Scope)+len(link.Ref.ID) > slackMessageLimit {
					return "", errors.New("slack notification link reference exceeds the message limit")
				}
				label = link.Ref.String()
			}
		}
		if link.URL == "" {
			parts = append(parts, markdownLabel.Replace(label))
			continue
		}
		_, ok := httpsURL(link.URL)
		if !ok || strings.ContainsAny(link.URL, "<>\\") || strings.ContainsFunc(link.URL, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) {
			return "", errors.New("slack notification links require an https url")
		}
		if label == "" {
			label = link.URL
		}
		parts = append(parts, "["+markdownLabel.Replace(label)+"](<"+link.URL+">)")
	}
	var nonempty []string
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			nonempty = append(nonempty, part)
		}
	}
	message := strings.Join(nonempty, "\n\n")
	if message == "" || len(message) > slackMessageLimit || !utf8.ValidString(message) ||
		strings.ContainsFunc(message, func(r rune) bool { return unicode.IsControl(r) && r != '\r' && r != '\t' && r != '\n' }) {
		return "", errors.New("slack notification requires a nonempty message within the byte limit")
	}
	return message, nil
}

// Deliver requires the caller's devflow policy gate and a durable receipt
// store shared with retries. authorize must verify the selected session's
// workspace and its live tool schema, and approve this exact intent. send
// must dispatch that call at most once on the verified session and return a
// successful result's JSON or an error. Tool errors and ambiguous outcomes
// leave a durable unconfirmed claim. The caller audits the resulting outcome.
func (n SlackNotification) Deliver(
	ctx context.Context,
	store notifications.ReceiptStore,
	authorize func(context.Context, SlackIntent, Call) error,
	send func(context.Context, Call) (json.RawMessage, error),
) (provider.DeliveryReceipt, error) {
	if n.intent.OperationID == "" || store == nil || authorize == nil || send == nil {
		return provider.DeliveryReceipt{}, provider.Unsupported(string(Slack), "send without a prepared notification, policy gate, durable receipts and caller")
	}
	if err := ctx.Err(); err != nil {
		return provider.DeliveryReceipt{}, err
	}
	if err := authorize(ctx, n.intent, n.call); err != nil {
		return provider.DeliveryReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return provider.DeliveryReceipt{}, err
	}

	key := notifications.ReceiptKey{OperationID: n.intent.OperationID, Destination: n.intent.Destination.Name}
	// Match the existing notification router's receipt identity. Bind the
	// native tool, provider instance, workspace, conversation and exact JSON
	// payload separately so changing routes or backends cannot replay a send.
	digest := sha256.Sum256(fmt.Appendf(nil, "%q %q", key.OperationID, key.Destination))
	fingerprint := sha256.Sum256(fmt.Appendf(nil, "%q %q %q %s", n.call.capability.Tool, n.call.instance,
		n.intent.Destination.WorkspaceID, n.call.args))
	receipt := provider.DeliveryReceipt{ID: hex.EncodeToString(digest[:])}
	state, err := store.Claim(ctx, key, fingerprint)
	switch {
	case errors.Is(err, provider.ErrConflict):
		return receipt, provider.ErrConflict
	case err != nil:
		return receipt, fmt.Errorf("%w: claim slack delivery", notifications.ErrReceiptStore)
	}
	switch state {
	case notifications.Delivered:
		receipt.Status = provider.AlreadyApplied
		return receipt, nil
	case notifications.Unconfirmed:
		return receipt, notifications.ErrUnconfirmed
	case notifications.Claimed:
		return n.deliverClaimed(ctx, store, key, receipt, send)
	default:
		return receipt, fmt.Errorf("%w: invalid slack claim state", notifications.ErrReceiptStore)
	}
}

func (n SlackNotification) deliverClaimed(
	ctx context.Context,
	store notifications.ReceiptStore,
	key notifications.ReceiptKey,
	receipt provider.DeliveryReceipt,
	send func(context.Context, Call) (json.RawMessage, error),
) (provider.DeliveryReceipt, error) {
	if err := ctx.Err(); err != nil {
		return receipt, errors.Join(err, finishSlackClaim(ctx, store, key, notifications.Retryable))
	}
	raw, err := send(ctx, n.call)
	if err != nil {
		return receipt, fmt.Errorf("%w: native slack send failed", notifications.ErrUnconfirmed)
	}
	receipt.URL, err = slackMessageLink(n.intent.Destination.ChannelID, raw)
	if err != nil {
		return receipt, errors.Join(notifications.ErrUnconfirmed, err)
	}
	receipt.Status = provider.Applied
	return receipt, finishSlackClaim(ctx, store, key, notifications.Delivered)
}

func finishSlackClaim(ctx context.Context, store notifications.ReceiptStore, key notifications.ReceiptKey, state notifications.ReceiptState) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), receiptTimeout)
	defer cancel()
	if err := store.Finish(finishCtx, key, state); err != nil {
		return fmt.Errorf("%w: record slack delivery outcome", notifications.ErrReceiptStore)
	}
	return nil
}

func slackMessageLink(channel string, raw json.RawMessage) (string, error) {
	var result struct {
		OK      *bool  `json:"ok"`
		Link    string `json:"message_link"`
		Context struct {
			ChannelID string `json:"channel_id"`
			MessageTS string `json:"message_ts"`
		} `json:"message_context"`
	}
	if err := decodeObject(raw, &result); err != nil {
		return "", err
	}
	u, ok := httpsURL(result.Link)
	if result.OK != nil && !*result.OK || !ok || !strings.HasSuffix(strings.ToLower(u.Host), ".slack.com") || u.RawQuery != "" ||
		u.Fragment != "" || result.Context.ChannelID != channel || !slackTS.MatchString(result.Context.MessageTS) ||
		u.EscapedPath() != "/archives/"+channel+"/p"+strings.ReplaceAll(result.Context.MessageTS, ".", "") {
		return "", fmt.Errorf("%w: slack acknowledgement does not identify the delivered message", ErrInvalidResult)
	}
	return u.String(), nil
}
