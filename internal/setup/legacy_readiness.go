package setup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/auth"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/provider/notifications"
)

func (p *ProviderSetupPlan) checkAtlassian(ctx context.Context, role selectedRole, site string,
	load func(string) (auth.Atlassian, error), check func(*atlassian.Client, context.Context) error, hint, ready string,
) {
	if err := atlassian.ValidateSite(site); err != nil {
		p.check(role.name, role.Provider, false, fmt.Sprintf("set %s.settings.site: %s", role.name, err))
		return
	}
	cred, err := load(site)
	if err != nil || cred.Token == "" || (!cred.Bearer && cred.Email == "") {
		p.check(role.name, role.Provider, false, "unavailable Atlassian credentials: "+hint)
		return
	}
	cred.Site = site
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = check(atlassian.New(cred), ctx)
	var apiErr *atlassian.APIError
	switch {
	case err == nil:
		p.check(role.name, role.Provider, true, ready)
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized:
		p.check(role.name, role.Provider, false, "Atlassian rejected the credentials (HTTP 401): "+hint)
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden:
		p.check(role.name, role.Provider, false, "Atlassian denied read access (HTTP 403): check product access and token permissions")
	case apiErr != nil:
		p.check(role.name, role.Provider, false, fmt.Sprintf("Atlassian read check returned HTTP %d: verify the configured site and service availability", apiErr.Status))
	default:
		p.check(role.name, role.Provider, false, "Atlassian read check failed: verify the configured site and network access")
	}
}

func (p *ProviderSetupPlan) checkSlack(role selectedRole, settings *config.SlackSettings) {
	ref := settings.WebhookCredentialRef(config.DefaultSlackWebhookEnv)
	endpoint := os.Getenv(ref)
	if endpoint == "" {
		p.check(role.name, role.Provider, false, fmt.Sprintf("supply an authorized Slack incoming webhook via %s in the devflow environment", ref))
		return
	}
	name := value(settings.Destination)
	if name == "" {
		name = "slack"
	}
	_, err := notifications.NewRouter(map[string]notifications.DestinationConfig{
		name: {Kind: notifications.Slack, URL: endpoint},
	}, nil, nil)
	if err != nil {
		p.check(role.name, role.Provider, false, fmt.Sprintf("check notifications.settings.destination and webhook in %s: %s", ref, err))
		return
	}
	p.check(role.name, role.Provider, true, "Slack webhook configuration is valid; delivery requires a separate send")
}
