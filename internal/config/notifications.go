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
	SlackSettings    `yaml:",inline"`
	HeaderRefs       map[string]string `yaml:"header_refs"`
	RejectedStatuses []int             `yaml:"rejected_statuses"`
}

type NotificationDestination struct {
	Provider         string            `yaml:"provider" allowed:"slack,webhook"`
	CredentialRef    string            `yaml:"credential_ref"`
	HeaderRefs       map[string]string `yaml:"header_refs"`
	RejectedStatuses []int             `yaml:"rejected_statuses"`
}

type NotificationRoutes struct {
	Destinations map[string]NotificationDestination `yaml:"destinations"`
	Routes       map[string][]string                `yaml:"routes"`
}

var (
	notificationName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	environmentName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidNotificationName reports whether a signal or destination has a stable name.
func ValidNotificationName(name string) bool { return notificationName.MatchString(name) }

func validateNotificationSettings(selection Selection) error {
	switch settings := selection.Settings.(type) {
	case *SlackSettings:
		return validateSingleNotification(*settings, NotificationDestination{})
	case *WebhookSettings:
		return validateSingleNotification(settings.SlackSettings, NotificationDestination{
			HeaderRefs: settings.HeaderRefs, RejectedStatuses: settings.RejectedStatuses,
		})
	case *NotificationRoutes:
		for _, name := range slices.Sorted(maps.Keys(settings.Destinations)) {
			destination := settings.Destinations[name]
			if !ValidNotificationName(name) {
				return errors.New("notifications.settings.destinations: invalid destination name")
			}
			if destination.Provider == "" || destination.CredentialRef == "" {
				return fmt.Errorf("notifications.settings.destinations.%s: provider and credential_ref are required", name)
			}
			if err := validateNotificationDestination(destination); err != nil {
				return fmt.Errorf("notifications.settings.destinations.%s: %w", name, err)
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

func validateSingleNotification(settings SlackSettings, destination NotificationDestination) error {
	if settings.CredentialRef != nil {
		destination.CredentialRef = *settings.CredentialRef
	}
	if err := validateNotificationDestination(destination); err != nil {
		return fmt.Errorf("notifications.settings: %w", err)
	}
	return nil
}

func validateNotificationDestination(destination NotificationDestination) error {
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
