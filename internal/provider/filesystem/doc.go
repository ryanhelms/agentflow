// Package filesystem implements offline task and knowledge providers over a
// directory of UTF-8 Markdown files. A Repository adapter can call
// NewTasks(instance, directory) and NewKnowledge(instance, directory) on a
// dedicated directory in its checkout, then stage the resulting Markdown changes.
// The directory must exist and its parent path must be trusted. Both constructors
// reject a symlink at the final component and pin the opened directory until Close.
//
// NewOKFCompatibleKnowledge also discovers OKF concepts recursively under the
// same pinned root. Managed concepts keep their scope and ID across renames.
// OKF concepts are read-only. New and replacement writes use the v1 format.
//
// In v1 storage only top-level *.md files (case-insensitive) participate. Each must be a
// regular file with a single version 1 YAML mapping between two --- lines.
// A task named backlog.md can have the following contents.
//
//	---
//	version: 1
//	kind: task
//	instance: local
//	scope: engineering
//	id: T-42
//	title: Review the rollout
//	state: {id: open, name: Open, terminal: false}
//	---
//	Confirm that the release is ready.
//
// A document uses kind "document" and omits state. The Markdown body is the task
// description or document body, including its original trailing newline, if any.
// Task metadata also supports type, priority, assignee, labels and an optional
// template mapping with its exact name and revision. Both kinds
// support links (title, url, ref) and operations (operation ID to request digest).
// Keep operations and the identity fields intact across edits. Scope and ID are
// opaque values. The full identity is (kind, instance, scope, id), independent of
// filename. An empty Find scope searches the instance. A zero limit returns all
// matches. AssignedToMe returns provider.ErrUnsupported because the adapter
// has no configured user identity.
//
// Writes publish a synced sibling file via exclusive hard link on creation or
// rename on replacement. Use a local filesystem supporting these operations.
// ExpectedRevision is the SHA-256 of the exact file bytes. Publish requires it
// for replacement. Transition can update just the state without it. A repeated
// OperationID within the same kind, instance and scope returns AlreadyApplied
// with the current revision, even after intervening writes. Reusing the ID for
// different input returns provider.ErrConflict. Receipts remain in the record.
//
// Writers in this package coordinate through a kernel lock on the directory
// (on Windows, on a persistent .devflow-write.lock file). Independent editors
// must serialize concurrent mutations with these writers. Leave the Windows lock
// file in place and exclude it and .devflow-stage-* from version control.
// Interrupted staging files are ignored. Windows syncs the file contents only.
// Each operation scans the directory. These adapters suit small local collections.
package filesystem
