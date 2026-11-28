// Package ai is "Draft with AI" (ADR "Phase 8" in
// docs/knowledge/engineering/decisions/event-driven-platform.md): a
// harness writes text for one existing operation, and agentflow turns that
// text into candidates for the operation. A draft is words, never an
// action.
//
//   - A Feature (internal/ai/features) gathers context through read-only
//     app methods, gives constant instructions and an answer struct, and
//     turns a decoded answer into candidates.
//   - Prepare (the Template Method in run.go) routes the feature to the
//     person's harness (Route), filters every part (Filter), builds the
//     prompt (PromptBuilder) and returns the Manifest: what would be sent,
//     by source, size and hash, never the data.
//   - Start runs a phase 7 session with the draft profile
//     (aisession.StartRequest.Draft): no file, terminal or agentflow tool,
//     no person to ask, an empty working folder.
//   - Finish runs in the session host after the one prompt turn: it parses
//     the answer, cleans every field and calls Feature.Candidates. It is
//     pure.
//
// Nothing in this package applies a candidate. It cannot import the
// Invoker or the gate (TestAIImports), and it calls only read methods of
// the App (TestDraftNeverWrites). Using a candidate is the caller's own
// call to the existing operation, through that operation's own flow.
//
// Every byte of context and every byte of the answer is untrusted data.
package ai
