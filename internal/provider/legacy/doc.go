// Package legacy adapts agentflow's existing hosted service clients to the provider
// contracts. Callers supply clients and configured instance names.
//
// Jira can create an issue with one related Jira issue. Explicit priority,
// external or multiple links, revision preconditions and operation IDs require
// capabilities the Jira client lacks and return provider.ErrUnsupported.
// Jira transitions recheck status before sending the write. The client's
// transition request has no atomic status precondition.
//
// Confluence searches pages, reads qualified references and replaces existing
// pages with an expected version and a receipt in the version message.
//
// GitHub supports GitHub.com pull requests and unresolved review threads.
package legacy
