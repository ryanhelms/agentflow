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
		return selection.validateSingleNotification(settings.Destination, settings.CredentialRef, NotificationDestination{
			Provider: "slack", Native: settings.Native,
		})
	case *WebhookSettings:
		return selection.validateSingleNotification(settings.Destination, settings.CredentialRef, NotificationDestination{
			Provider:   "webhook",
			HeaderRefs: settings.HeaderRefs, RejectedStatuses: settings.RejectedStatuses,
		})
	case *NotificationRoutes:
		nativeScopes := map[[2]string]string{}
		for _, name := range slices.Sorted(maps.Keys(settings.Destinations)) {
			destination := settings.Destinations[name]
			path := "destinations." + escapeSettingKey.Replace(name)
			if !ValidNotificationName(name) {
				return selection.settingError("notifications", path, errors.New("invalid destination name"))
			}
			if destination.Provider == "" || (destination.Native == nil && destination.CredentialRef == "") {
				return selection.settingError("notifications", path, errors.New("provider and either native scope or credential_ref are required"))
			}
			if err := selection.validateNotificationDestination(path+".", destination); err != nil {
				return err
			}
			if scope := destination.Native; scope != nil {
				key := [2]string{scope.WorkspaceID, scope.ChannelID}
				if first, exists := nativeScopes[key]; exists {
					return selection.settingError("notifications", path+".native", fmt.Errorf("native scope already used by %s", first))
				}
				nativeScopes[key] = name
			}
		}
		for _, signal := range slices.Sorted(maps.Keys(settings.Routes)) {
			path := "routes." + escapeSettingKey.Replace(signal)
			if !ValidNotificationName(signal) {
				return selection.settingError("notifications", path, errors.New("invalid signal name"))
			}
			for _, name := range settings.Routes[signal] {
				if _, ok := settings.Destinations[name]; !ok {
					return selection.settingError("notifications", path, errors.New("destination is not defined"))
				}
			}
		}
	}
	return nil
}

func (sel Selection) validateSingleNotification(name, credentialRef *string, destination NotificationDestination) error {
	if credentialRef != nil {
		destination.CredentialRef = *credentialRef
	}
	if destination.Native != nil && name != nil && !ValidNotificationName(*name) {
		return sel.settingError("notifications", "destination", errors.New("invalid destination name"))
	}
	return sel.validateNotificationDestination("", destination)
}

func (sel Selection) validateNotificationDestination(prefix string, destination NotificationDestination) error {
	invalid := func(property, reason string) error {
		return sel.settingError("notifications", prefix+property, errors.New(reason))
	}
	if scope := destination.Native; scope != nil {
		if destination.Provider != "slack" {
			return invalid("native", "only the slack provider supports native delivery")
		}
		if destination.CredentialRef != "" || len(destination.HeaderRefs) != 0 || len(destination.RejectedStatuses) != 0 {
			return invalid("native", "webhook settings cannot be used with native slack delivery")
		}
		switch {
		case !slackWorkspaceID.MatchString(scope.WorkspaceID):
			return invalid("native.workspace_id", "expected a Slack workspace ID")
		case !slackChannelID.MatchString(scope.ChannelID):
			return invalid("native.channel_id", "expected a Slack conversation ID")
		case scope.Server != "" && !ValidNotificationName(scope.Server):
			return invalid("native.server", "expected an MCP registration name")
		case scope.ActorID != "" && !slackActorID.MatchString(scope.ActorID):
			return invalid("native.actor_id", "expected a Slack user ID")
		}
		return nil
	}
	if destination.CredentialRef != "" && !environmentName.MatchString(destination.CredentialRef) {
		return invalid("credential_ref", "expected an environment variable name")
	}
	for name, ref := range destination.HeaderRefs {
		if !environmentName.MatchString(ref) {
			return invalid("header_refs."+escapeSettingKey.Replace(name), "expected environment variable names")
		}
	}
	for _, status := range destination.RejectedStatuses {
		if status < 400 || status >= 500 || status == http.StatusRequestTimeout {
			return invalid("rejected_statuses", "expected a 4xx status other than 408")
		}
	}
	return nil
}
