package config

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
)

type WebhookSettings struct {
	Destination      *string           `yaml:"destination"`
	CredentialRef    *string           `yaml:"credential_ref"`
	HeaderRefs       map[string]string `yaml:"header_refs"`
	RejectedStatuses []int             `yaml:"rejected_statuses"`
}

// SlackNativeSettings scopes an authenticated Slack MCP registration to an
// existing conversation. ActorID optionally requires a specific Slack user.
type SlackNativeSettings struct {
	Server      string `yaml:"server" json:"server,omitempty"`
	WorkspaceID string `yaml:"workspace_id" json:"workspace_id"`
	ChannelID   string `yaml:"channel_id" json:"channel_id"`
	ActorID     string `yaml:"actor_id" json:"actor_id,omitempty"`
}

type NotificationDestination struct {
	Provider         string               `yaml:"provider" allowed:"slack,webhook"`
	CredentialRef    string               `yaml:"credential_ref"`
	Native           *SlackNativeSettings `yaml:"native"`
	HeaderRefs       map[string]string    `yaml:"header_refs"`
	RejectedStatuses []int                `yaml:"rejected_statuses"`
}

type NotificationRoutes struct {
	Destinations map[string]NotificationDestination `yaml:"destinations"`
	Routes       map[string][]string                `yaml:"routes"`
}

var (
	notificationName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	environmentName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	slackWorkspaceID = regexp.MustCompile(`^T[A-Z0-9]{8,31}$`)
	slackChannelID   = regexp.MustCompile(`^[CDG][A-Z0-9]{8,31}$`)
	slackActorID     = regexp.MustCompile(`^[UW][A-Z0-9]{8,31}$`)
)

// ValidNotificationName reports whether a signal or destination has a stable name.
func ValidNotificationName(name string) bool { return notificationName.MatchString(name) }

func validateNotificationSettings(selection Selection) error {
	switch settings := selection.Settings.(type) {
	case *SlackSettings:
		return validateSingleNotification(settings.Destination, settings.CredentialRef, NotificationDestination{
			Provider: "slack", Native: settings.Native,
		})
	case *WebhookSettings:
		return validateSingleNotification(settings.Destination, settings.CredentialRef, NotificationDestination{
			Provider:   "webhook",
			HeaderRefs: settings.HeaderRefs, RejectedStatuses: settings.RejectedStatuses,
		})
	case *NotificationRoutes:
		nativeScopes := map[[2]string]string{}
		for _, name := range slices.Sorted(maps.Keys(settings.Destinations)) {
			destination := settings.Destinations[name]
			if !ValidNotificationName(name) {
				return errors.New("notifications.settings.destinations: invalid destination name")
			}
			if destination.Provider == "" || (destination.Native == nil && destination.CredentialRef == "") {
				return fmt.Errorf("notifications.settings.destinations.%s: provider and either native scope or credential_ref are required", name)
			}
			if err := validateNotificationDestination(destination); err != nil {
				return fmt.Errorf("notifications.settings.destinations.%s: %w", name, err)
			}
			if scope := destination.Native; scope != nil {
				key := [2]string{scope.WorkspaceID, scope.ChannelID}
				if first, exists := nativeScopes[key]; exists {
					return fmt.Errorf("notifications.settings.destinations.%s: native scope already used by %s", name, first)
				}
				nativeScopes[key] = name
			}
		}
		for _, signal := range slices.Sorted(maps.Keys(settings.Routes)) {
			if !ValidNotificationName(signal) {
				return errors.New("notifications.settings.routes: invalid signal name")
			}
			for _, name := range settings.Routes[signal] {
				if _, ok := settings.Destinations[name]; !ok {
					return fmt.Errorf("notifications.settings.routes.%s: destination is not defined", signal)
				}
			}
		}
	}
	return nil
}

func validateSingleNotification(name, credentialRef *string, destination NotificationDestination) error {
	if credentialRef != nil {
		destination.CredentialRef = *credentialRef
	}
	if destination.Native != nil && name != nil && !ValidNotificationName(*name) {
		return errors.New("notifications.settings.destination: invalid destination name")
	}
	if err := validateNotificationDestination(destination); err != nil {
		return fmt.Errorf("notifications.settings: %w", err)
	}
	return nil
}

func validateNotificationDestination(destination NotificationDestination) error {
	if scope := destination.Native; scope != nil {
		if destination.Provider != "slack" {
			return errors.New("native: only the slack provider supports native delivery")
		}
		if destination.CredentialRef != "" || len(destination.HeaderRefs) != 0 || len(destination.RejectedStatuses) != 0 {
			return errors.New("native: webhook settings cannot be used with native slack delivery")
		}
		return scope.validate()
	}
	if destination.CredentialRef != "" && !environmentName.MatchString(destination.CredentialRef) {
		return errors.New("credential_ref: expected an environment variable name")
	}
	for _, ref := range destination.HeaderRefs {
		if !environmentName.MatchString(ref) {
			return errors.New("header_refs: expected environment variable names")
		}
	}
	for _, status := range destination.RejectedStatuses {
		if status < 400 || status >= 500 || status == http.StatusRequestTimeout {
			return errors.New("rejected_statuses: expected a 4xx status other than 408")
		}
	}
	return nil
}

func (s SlackNativeSettings) validate() error {
	if !slackWorkspaceID.MatchString(s.WorkspaceID) {
		return errors.New("native.workspace_id: expected a Slack workspace ID")
	}
	if !slackChannelID.MatchString(s.ChannelID) {
		return errors.New("native.channel_id: expected a Slack conversation ID")
	}
	if s.Server != "" && !ValidNotificationName(s.Server) {
		return errors.New("native.server: expected an MCP registration name")
	}
	if s.ActorID != "" && !slackActorID.MatchString(s.ActorID) {
		return errors.New("native.actor_id: expected a Slack user ID")
	}
	return nil
}
