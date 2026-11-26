package notifications

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ryanhelms/agentflow/internal/provider"
)

type ReceiverKind string

const (
	Slack   ReceiverKind = "slack"
	Webhook ReceiverKind = "webhook"
)

// DestinationConfig contains resolved settings. URL and Headers may contain
// credentials and are used only to build the outgoing request. Use HTTPS when
// sending credentials. RejectedStatuses lists additional 4xx responses the
// receiver guarantees occurred before acceptance. A 408 is always ambiguous.
type DestinationConfig struct {
	Kind             ReceiverKind
	URL              string
	Headers          http.Header
	RejectedStatuses []int
}

type receiver struct {
	kind     ReceiverKind
	url      string
	headers  http.Header
	rejected map[int]bool
}

func newReceiver(config DestinationConfig) (receiver, error) {
	if config.Kind != Slack && config.Kind != Webhook {
		return receiver{}, fmt.Errorf("notification receiver kind: %w", provider.ErrUnsupported)
	}
	endpoint, err := url.Parse(config.URL)
	if err != nil || !isHTTPURL(endpoint) || endpoint.User != nil || strings.Contains(config.URL, "#") {
		return receiver{}, errors.New("notification endpoint requires an absolute http or https URL without user info or a fragment")
	}

	headers := make(http.Header, len(config.Headers)+1)
	for name, values := range config.Headers {
		if !validHeaderName(name) {
			return receiver{}, errors.New("notification header has an invalid name")
		}
		switch http.CanonicalHeaderKey(name) {
		case "Content-Type", "Host", "Content-Length", "Transfer-Encoding", "Trailer":
			return receiver{}, errors.New("notification header overrides a managed request field")
		}
		for _, value := range values {
			if strings.ContainsFunc(value, func(r rune) bool { return r < ' ' && r != '\t' || r == '\x7f' }) {
				return receiver{}, errors.New("notification header has an invalid value")
			}
			headers.Add(name, value)
		}
	}
	headers.Set("Content-Type", "application/json")

	rejected := make(map[int]bool, len(config.RejectedStatuses))
	if config.Kind == Slack {
		// Incoming webhooks use these responses to reject requests.
		for _, status := range []int{400, 403, 404, 410, 429} {
			rejected[status] = true
		}
	}
	for _, status := range config.RejectedStatuses {
		if status < 400 || status >= 500 || status == http.StatusRequestTimeout {
			return receiver{}, errors.New("notification rejection status must be a 4xx other than 408")
		}
		rejected[status] = true
	}

	return receiver{kind: config.Kind, url: endpoint.String(), headers: headers, rejected: rejected}, nil
}

func isHTTPURL(endpoint *url.URL) bool {
	return endpoint != nil && (endpoint.Scheme == "https" || endpoint.Scheme == "http") && endpoint.Hostname() != ""
}

func validHeaderName(name string) bool {
	const token = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$%&'*+-.^`_|~"
	return name != "" && !strings.ContainsFunc(name, func(r rune) bool { return !strings.ContainsRune(token, r) })
}

func (r receiver) request(ctx context.Context, notification provider.Notification) (*http.Request, [sha256.Size]byte, error) {
	var payload any = struct {
		Destination string          `json:"destination"`
		Title       string          `json:"title"`
		Body        string          `json:"body"`
		Links       []provider.Link `json:"links,omitempty"`
	}{notification.Destination, notification.Title, notification.Body, notification.Links}
	if r.kind == Slack {
		payload = struct {
			Text string `json:"text"`
		}{slackText(notification)}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, [sha256.Size]byte{}, errors.New("encode notification payload")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, [sha256.Size]byte{}, errors.New("build notification request")
	}
	request.Header = r.headers.Clone()
	// A known request size with no GetBody prevents the default transport
	// from replaying the POST, even with a caller-supplied idempotency header.
	request.GetBody = nil
	fingerprint := sha256.Sum256(append([]byte(r.kind+"\x00"), body...))
	return request, fingerprint, nil
}

func (r receiver) post(client *http.Client, request *http.Request) (provider.DeliveryReceipt, error) {
	response, err := client.Do(request)
	if err != nil {
		// HTTP errors can include the webhook URL and other sensitive values.
		return provider.DeliveryReceipt{}, fmt.Errorf("%w: webhook request failed", ErrUnconfirmed)
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return provider.DeliveryReceipt{Status: provider.Applied}, nil
	}
	outcome := ErrUnconfirmed
	if r.rejected[response.StatusCode] {
		outcome = ErrRejected
	}
	return provider.DeliveryReceipt{}, fmt.Errorf("%w: webhook returned HTTP %d", outcome, response.StatusCode)
}

var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func slackText(notification provider.Notification) string {
	lines := make([]string, 0, 2+len(notification.Links))
	for _, line := range []string{notification.Title, notification.Body} {
		if line != "" {
			lines = append(lines, line)
		}
	}
	for _, link := range notification.Links {
		label := link.Title
		if label == "" && link.Ref != nil {
			label = link.Ref.String()
		}
		endpoint, err := url.Parse(link.URL)
		if err != nil || !isHTTPURL(endpoint) || endpoint.User != nil || strings.ContainsAny(link.URL, "<>|\r\n") {
			if label != "" {
				lines = append(lines, slackEscaper.Replace(label))
			}
			continue
		}
		target := slackEscaper.Replace(endpoint.String())
		if label != "" {
			target += "|" + slackEscaper.Replace(strings.ReplaceAll(label, "|", " "))
		}
		lines = append(lines, "<"+target+">")
	}
	return strings.Join(lines, "\n")
}
