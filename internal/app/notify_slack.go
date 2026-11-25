package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
	"github.com/nscaledev/devflow/internal/provider/notifications"
)

type nativeSlackPlan struct {
	Intent  nativemap.SlackIntent `json:"intent"`
	Call    native.Call           `json:"call"`
	Binding native.WriteBinding   `json:"binding"`
	ActorID string                `json:"verified_actor_id,omitempty"`
}

func prepareNativeSlack(ctx context.Context, scope config.SlackNativeSettings, notification provider.Notification, id string,
	store notifications.ReceiptStore,
) (notificationDelivery, *nativeSlackPlan, error) {
	server := scope.Server
	if server == "" {
		server = "slack"
	}
	prepared, err := nativemap.PrepareSlackNotification(nativemap.SlackDestination{
		Name: notification.Destination, Instance: server, WorkspaceID: scope.WorkspaceID, ChannelID: scope.ChannelID,
	}, notification, provider.WriteOptions{OperationID: id})
	if err != nil {
		return nil, nil, err
	}
	mapped := prepared.Call()
	call := native.Call{Provider: string(mapped.Capability().Kind), Server: mapped.Instance(), Tool: mapped.Capability().Tool}
	if err := json.Unmarshal(mapped.Arguments(), &call.Arguments); err != nil {
		return nil, nil, errors.New("invalid prepared slack arguments")
	}
	binding, err := native.PrepareWrite(ctx, call)
	if err != nil {
		return nil, nil, slackPreparationError(err)
	}
	if binding.Provider != call.Provider || binding.Server != call.Server || binding.Tool != call.Tool ||
		binding.Connection == "" || (binding.Origin != "https://mcp.slack.com" && binding.Origin != "https://mcp.slack.com/") {
		return nil, nil, fmt.Errorf("invalid authenticated slack binding: %w", native.ErrDenied)
	}
	identity, err := verifySlackScope(ctx, binding, scope)
	if err != nil {
		return nil, nil, err
	}
	plan := &nativeSlackPlan{Intent: prepared.Intent(), Call: call, Binding: binding, ActorID: identity.ActorID}
	// notify invokes this callback only after policy approval of the plan above.
	deliver := func(ctx context.Context) (provider.DeliveryReceipt, error) {
		return prepared.Deliver(ctx, store, func(_ context.Context, intent nativemap.SlackIntent, _ nativemap.Call) error {
			if intent != plan.Intent {
				return provider.ErrConflict
			}
			return nil
		}, func(ctx context.Context, _ nativemap.Call) (json.RawMessage, error) {
			raw, err := native.Write(ctx, binding, call)
			if err != nil {
				return nil, err
			}
			return native.Payload(raw)
		})
	}
	return deliver, plan, nil
}

func verifySlackScope(ctx context.Context, binding native.WriteBinding, scope config.SlackNativeSettings) (nativemap.SlackIdentity, error) {
	read := func(tool string, args map[string]any) (json.RawMessage, error) {
		raw, err := native.ReadBound(ctx, binding, native.Call{Provider: binding.Provider, Server: binding.Server, Tool: tool, Arguments: args})
		if err != nil {
			return nil, err
		}
		return native.Payload(raw)
	}
	var identity nativemap.SlackIdentity
	raw, err := read("slack_read_user_profile", map[string]any{})
	if err == nil {
		identity, err = nativemap.DecodeSlackIdentity(raw)
	}
	if err != nil && !errors.Is(err, native.ErrUnavailable) {
		return identity, slackPreparationError(err)
	}
	if scope.ActorID != "" && identity.ActorID != scope.ActorID {
		return identity, fmt.Errorf("could not verify the configured slack actor: %w", native.ErrDenied)
	}
	if identity.WorkspaceID != "" && identity.WorkspaceID != scope.WorkspaceID {
		return identity, fmt.Errorf("authenticated slack workspace differs from the configured workspace: %w", native.ErrDenied)
	}
	return identity, verifySlackWorkspaceChannel(read, scope)
}

func verifySlackWorkspaceChannel(read func(string, map[string]any) (json.RawMessage, error), scope config.SlackNativeSettings) error {
	channelTypes := "public_channel,private_channel"
	switch scope.ChannelID[0] {
	case 'D':
		channelTypes = "im"
	case 'G':
		channelTypes = "private_channel,mpim"
	}
	args := map[string]any{"team_id": scope.WorkspaceID, "types": channelTypes, "exclude_archived": true, "format": "names_only"}
	seen := map[string]bool{}
	for range 8 {
		raw, err := read("slack_list_user_channels", args)
		if err != nil {
			return slackPreparationError(err)
		}
		found, cursor, err := nativemap.FindSlackWorkspaceChannel(raw, scope.WorkspaceID, scope.ChannelID)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
		if cursor == "" || seen[cursor] {
			break
		}
		seen[cursor] = true
		args["cursor"] = cursor
	}
	return fmt.Errorf("could not verify the slack conversation in the configured workspace: %w", provider.ErrNotFound)
}

func slackPreparationError(err error) error {
	switch {
	case errors.Is(err, native.ErrUnavailable):
		return fmt.Errorf("required authenticated slack tools are unavailable: %w", native.ErrUnavailable)
	case errors.Is(err, native.ErrSignIn):
		return fmt.Errorf("sign in via the client's slack MCP connection: %w", native.ErrSignIn)
	case errors.Is(err, native.ErrDenied):
		return fmt.Errorf("slack MCP access or scope verification was denied: %w", native.ErrDenied)
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return errors.New("could not verify the authenticated slack connection and tool")
	}
}
