// Package legacy adapts devflow's existing hosted service clients to the provider
// contracts. Callers supply clients and configured instance names.
//
// Jira can create an issue with one related Jira issue. Explicit priority,
// external or multiple links, revision preconditions and operation IDs require
// capabilities the Jira client lacks and return provider.ErrUnsupported.
// Jira transitions recheck status before sending the write. The client's
// transition request has no atomic status precondition.
//
// Confluence only exposes search excerpts. Reading a page by ID and publishing
// return provider.ErrUnsupported.
//
// GitHub supports GitHub.com pull requests and unresolved review threads. The
// legacy lookup is limited to heads owned by the target repository's owner.
package legacy
