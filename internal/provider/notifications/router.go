// Package notifications routes provider notifications to explicitly configured
// webhook destinations and reconciles deliveries by operation ID.
package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/nscaledev/devflow/internal/provider"
)

const requestTimeout = 15 * time.Second

var (
	ErrUnconfirmed  = errors.New("notification delivery is unconfirmed")
	ErrRejected     = errors.New("notification was rejected")
	ErrReceiptStore = errors.New("notification receipt store failed")
)

// ReceiptKey identifies one logical delivery. Callers sharing a store must
// namespace operation IDs and keep destination names stable for replays.
type ReceiptKey struct {
	OperationID string
	Destination string
}

type ReceiptState string

const (
	// Claimed means this caller owns the attempt. Only Claim can return it.
	Claimed ReceiptState = "claimed"
	// Unconfirmed means an attempt is underway or its outcome is unknown.
	Unconfirmed ReceiptState = "unconfirmed"
	// Delivered means the receiver acknowledged the POST.
	Delivered ReceiptState = "delivered"
	// Retryable means the receiver did not accept the attempt. Only Finish takes it.
	Retryable ReceiptState = "retryable"
)

// ReceiptStore must persist changes before returning and coordinate all callers
// sharing its receipts. Claim atomically binds the payload fingerprint to the
// key and returns Claimed for a new key or a Retryable record, recording it as
// Unconfirmed before returning. It returns Delivered or Unconfirmed for existing
// records in those states. A different fingerprint for an existing key returns
// provider.ErrConflict, including after a Retryable outcome.
//
// Finish is called only by the owner of a claim and atomically replaces its
// Unconfirmed state with Delivered or Retryable. Claims do not expire: a crashed
// process may have sent the POST before it could record the outcome.
type ReceiptStore interface {
	Claim(ctx context.Context, key ReceiptKey, fingerprint [sha256.Size]byte) (ReceiptState, error)
	Finish(ctx context.Context, key ReceiptKey, state ReceiptState) error
}

// Router delivers to a single named destination or fans out to an explicit list.
// It can be shared across goroutines when its store and transport support it.
type Router struct {
	destinations map[string]receiver
	client       *http.Client
	store        ReceiptStore
}

var _ provider.Notifications = (*Router)(nil)

// NewRouter copies the destination configuration. A nil transport uses
// http.DefaultTransport. An injected transport must honor request cancellation
// and must not retry POSTs with an unknown outcome. A store is required for
// deliveries with an operation ID.
func NewRouter(destinations map[string]DestinationConfig, store ReceiptStore, transport http.RoundTripper) (*Router, error) {
	router := &Router{
		destinations: make(map[string]receiver, len(destinations)),
		store:        store,
		client: &http.Client{
			Transport: transport,
			Timeout:   requestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	for name, config := range destinations {
		if strings.TrimSpace(name) == "" || strings.ContainsFunc(name, unicode.IsControl) {
			return nil, errors.New("notification destination requires a nonempty name without control characters")
		}
		receiver, err := newReceiver(config)
		if err != nil {
			return nil, err
		}
		router.destinations[name] = receiver
	}
	return router, nil
}

// DeliveryResult is the outcome for one destination. A receipt can accompany
// a store error if the POST succeeded but recording that success failed.
type DeliveryResult struct {
	Destination string
	Receipt     provider.DeliveryReceipt
	Err         error
}

// FanOut attempts each distinct destination in input order. Failures are
// reported per destination. The list supplies the destination for each copy of
// the notification, and an empty list produces no deliveries.
func (r *Router) FanOut(ctx context.Context, destinations []string, notification provider.Notification, options provider.WriteOptions) []DeliveryResult {
	results := make([]DeliveryResult, 0, len(destinations))
	seen := make(map[string]bool, len(destinations))
	for _, destination := range destinations {
		if seen[destination] {
			continue
		}
		seen[destination] = true
		notification.Destination = destination
		receipt, err := r.Deliver(ctx, notification, options)
		results = append(results, DeliveryResult{Destination: destination, Receipt: receipt, Err: err})
	}
	return results
}

// Deliver performs at most one POST. An empty operation ID bypasses receipts,
// so callers must supply a stable ID when the operation may be replayed.
func (r *Router) Deliver(ctx context.Context, notification provider.Notification, options provider.WriteOptions) (provider.DeliveryReceipt, error) {
	if options.ExpectedRevision != "" {
		return provider.DeliveryReceipt{}, fmt.Errorf("notification revision precondition: %w", provider.ErrUnsupported)
	}
	receiver, ok := r.destinations[notification.Destination]
	if !ok {
		return provider.DeliveryReceipt{}, fmt.Errorf("notification destination is not configured: %w", provider.ErrNotFound)
	}

	request, fingerprint, err := receiver.request(ctx, notification)
	if err != nil {
		return provider.DeliveryReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return provider.DeliveryReceipt{}, err
	}
	if options.OperationID == "" {
		return receiver.post(r.client, request)
	}
	if r.store == nil {
		return provider.DeliveryReceipt{}, fmt.Errorf("%w: operation ID requires a receipt store", ErrReceiptStore)
	}

	key := ReceiptKey{OperationID: options.OperationID, Destination: notification.Destination}
	id := receiptID(key)
	state, err := r.store.Claim(ctx, key, fingerprint)
	if errors.Is(err, provider.ErrConflict) {
		return provider.DeliveryReceipt{ID: id}, fmt.Errorf("notification operation ID was reused with a different payload: %w", provider.ErrConflict)
	}
	if err != nil {
		return provider.DeliveryReceipt{ID: id}, fmt.Errorf("%w: claim delivery", ErrReceiptStore)
	}

	switch state {
	case Delivered:
		return provider.DeliveryReceipt{ID: id, Status: provider.AlreadyApplied}, nil
	case Unconfirmed:
		return provider.DeliveryReceipt{ID: id}, ErrUnconfirmed
	case Claimed:
		return r.deliverClaimed(ctx, key, id, receiver, request)
	default:
		return provider.DeliveryReceipt{}, fmt.Errorf("%w: invalid claim state", ErrReceiptStore)
	}
}

func (r *Router) deliverClaimed(ctx context.Context, key ReceiptKey, id string, receiver receiver, request *http.Request) (provider.DeliveryReceipt, error) {
	receipt := provider.DeliveryReceipt{ID: id}
	err := ctx.Err()
	if err == nil {
		receipt, err = receiver.post(r.client, request)
		receipt.ID = id
	}
	if errors.Is(err, ErrUnconfirmed) {
		return receipt, err
	}
	state := Delivered
	if err != nil {
		state = Retryable
	}

	// The caller may have canceled after the POST was accepted. Finalize the
	// claim independently so subsequent callers can still reconcile delivery.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
	defer cancel()
	if finishErr := r.store.Finish(finishCtx, key, state); finishErr != nil {
		return receipt, errors.Join(err, fmt.Errorf("%w: record delivery outcome", ErrReceiptStore))
	}
	return receipt, err
}

func receiptID(key ReceiptKey) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%q %q", key.OperationID, key.Destination))
	return hex.EncodeToString(sum[:])
}
