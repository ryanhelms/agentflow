// Package provider defines agentflow's service objects and the operations its
// application code can request from provider adapters.
package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrUnsupported identifies an operation the selected provider cannot perform.
var ErrUnsupported = errors.New("provider does not support this operation")

// Unsupported adds details suitable for displaying to a caller.
func Unsupported(instance, operation string) error {
	return fmt.Errorf("%s: %s: %w", instance, operation, ErrUnsupported)
}

var (
	ErrNotFound = errors.New("provider resource not found")
	ErrConflict = errors.New("provider resource changed")
)

// WriteStatus is the observed outcome of a provider write.
type WriteStatus string

const (
	Applied        WriteStatus = "applied"
	AlreadyApplied WriteStatus = "already_applied"
	PendingReview  WriteStatus = "pending_review"
)

// WriteResult supports synchronous and review-based providers. Revision can be
// supplied as a precondition on a later write. URL points to the result or
// to the change request when Status is PendingReview.
type WriteResult struct {
	Ref      Ref         `json:"ref"`
	Status   WriteStatus `json:"status"`
	Revision string      `json:"revision,omitempty"`
	URL      string      `json:"url,omitempty"`
	Branch   string      `json:"branch,omitempty"`
}

// WriteOptions can carry a stable operation key and expected current revision
// across retries. Adapters unable to enforce a requested precondition must
// return ErrUnsupported.
type WriteOptions struct {
	OperationID      string
	ExpectedRevision string
	// CheckTask validates the latest task read before a comment or transition.
	// It is a read-time check, not an atomic server-side revision precondition.
	CheckTask func(Task) error `json:"-"`
}

type Link struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
	Ref   *Ref   `json:"ref,omitempty"`
}

type TaskState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category,omitempty"`
	Terminal bool   `json:"terminal,omitempty"`
}

type Task struct {
	Template    *TaskTemplateRef `json:"template,omitempty"`
	Labels      []string         `json:"labels,omitempty"`
	Ref         Ref              `json:"ref"`
	Key         string           `json:"key,omitempty"`
	URL         string           `json:"url,omitempty"`
	Title       string           `json:"title"`
	Description string           `json:"description,omitempty"`
	Type        string           `json:"type,omitempty"`
	State       TaskState        `json:"state"`
	Priority    string           `json:"priority,omitempty"`
	Assignee    string           `json:"assignee,omitempty"`
	Links       []Link           `json:"links,omitempty"`
	Revision    string           `json:"revision,omitempty"`
}

type TaskQuery struct {
	Scope        string
	Text         string
	AssignedToMe bool
	OpenOnly     bool
	Limit        int
}

type TaskDraft struct {
	Template    *TaskTemplateRef
	Scope       string
	Title       string
	Description string
	Type        string
	Priority    string
	Labels      []string
	Links       []Link
}

// Tasks describes the operations expected of a task management adapter.
type Tasks interface {
	Get(ctx context.Context, ref Ref) (Task, error)
	Find(ctx context.Context, query TaskQuery) ([]Task, error)
	Create(ctx context.Context, draft TaskDraft, options WriteOptions) (WriteResult, error)
	Transition(ctx context.Context, ref Ref, target TaskState, options WriteOptions) (WriteResult, error)
}

// TaskComments and TaskAssignment are optional task capabilities.
type TaskComments interface {
	Comment(ctx context.Context, ref Ref, body string, options WriteOptions) (WriteResult, error)
}

type TaskAssignment interface {
	AssignToMe(ctx context.Context, ref Ref, options WriteOptions) (WriteResult, error)
}

type Document struct {
	Ref         Ref      `json:"ref"`
	Title       string   `json:"title"`
	Body        string   `json:"body,omitempty"`
	URL         string   `json:"url,omitempty"`
	Revision    string   `json:"revision,omitempty"`
	Links       []Link   `json:"links,omitempty"`
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type DocumentQuery struct {
	Scope string
	Text  string
	Limit int
}

type DocumentDraft struct {
	Title       string
	Body        string
	Links       []Link
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// Knowledge reads and publishes documents. The publish reference identifies the
// destination document even before its first revision exists.
type Knowledge interface {
	Get(ctx context.Context, ref Ref) (Document, error)
	Find(ctx context.Context, query DocumentQuery) ([]Document, error)
	Publish(ctx context.Context, ref Ref, draft DocumentDraft, options WriteOptions) (WriteResult, error)
}

type RepositoryRef struct {
	Host string
	Path string
}

type ChangeRequest struct {
	Repository RepositoryRef
	ID         string
	URL        string
	Title      string
	Head       string
	HeadSHA    string
	Base       string
	Draft      bool
}

type ChangeDraft struct {
	Title string
	Body  string
	Head  string
	Base  string
	Draft bool
}

type Review struct {
	ID     string
	Path   string
	Line   int
	Author string
	Body   string
	State  string
	URL    string
}

type CodeHosting interface {
	FindChange(ctx context.Context, repo RepositoryRef, head string) (ChangeRequest, error)
	OpenChange(ctx context.Context, repo RepositoryRef, draft ChangeDraft) (ChangeRequest, error)
	Reviews(ctx context.Context, change ChangeRequest) ([]Review, error)
}

type CheckRun struct {
	Name       string
	State      string
	Conclusion string
	URL        string
}

type Checks interface {
	Checks(ctx context.Context, repo RepositoryRef, revision string) ([]CheckRun, error)
}

type Deployment struct {
	Environment string `json:"environment"`
	Revision    string `json:"revision"`
	State       string `json:"state"`
	URL         string `json:"url,omitempty"`
	ID          string `json:"id,omitempty"`
	Ref         string `json:"ref,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type Deployments interface {
	// Deployments returns records in newest-first order for the given revision.
	Deployments(ctx context.Context, repo RepositoryRef, revision string) ([]Deployment, error)
}

// ReleaseAsset describes an artifact attached by the build pipeline.
type ReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
	Size        int64  `json:"size"` // bytes
	Digest      string `json:"digest,omitempty"`
}

type Release struct {
	Tag      string         `json:"tag"`
	Revision string         `json:"revision"`
	Title    string         `json:"title"`
	Notes    string         `json:"notes"`
	URL      string         `json:"url,omitempty"`
	Assets   []ReleaseAsset `json:"assets,omitempty"` // read-only
}

type ReleaseReader interface {
	GetRelease(ctx context.Context, repo RepositoryRef, tag string) (Release, error)
}

type Releases interface {
	ReleaseReader
	PublishRelease(ctx context.Context, repo RepositoryRef, release Release, options WriteOptions) (WriteResult, error)
}

type Finding struct {
	Ref      Ref
	Title    string
	Body     string
	Severity string
	URL      string
}

type Incident struct {
	Ref      Ref
	Title    string
	Body     string
	Severity string
	URL      string
}

type Findings interface {
	Findings(ctx context.Context, since time.Time) ([]Finding, error)
}

type Incidents interface {
	Incidents(ctx context.Context, since time.Time) ([]Incident, error)
}

type Notification struct {
	Destination string
	Title       string
	Body        string
	Links       []Link
}

type DeliveryReceipt struct {
	ID     string
	Status WriteStatus
	URL    string
}

type Notifications interface {
	Deliver(ctx context.Context, notification Notification, options WriteOptions) (DeliveryReceipt, error)
}
