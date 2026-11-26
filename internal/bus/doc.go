// Package bus defines agentflow's in-process event bus: the event envelope,
// the cursor used to resume streams, topic patterns, and the Publisher,
// Subscriber, Point-to-Point Channel, Interceptor, Wire Tap and Dead Letter
// contracts.
//
// The contract is fixed by the ADR
// docs/knowledge/engineering/decisions/event-driven-platform.md. Change the
// ADR before changing anything exported here.
//
// Pattern names (GoF and Enterprise Integration Patterns):
//
//   - Event is an Event Message wrapped in an Envelope Wrapper.
//   - Publisher and Subscriber form a Publish-Subscribe Channel (GoF Observer).
//   - Queue is a Point-to-Point Channel with Competing Consumers.
//   - Interceptor is a Pipes and Filters chain (GoF Chain of Responsibility).
//   - Tap is a Wire Tap. The audit tap feeds policy.Gate.Audit.
//   - DeadLetter is a Dead Letter Channel.
//   - Topic and Pattern implement a Message Filter on each subscription.
//   - Cursor is the resume point for a Durable Subscriber.
//
// Package bustest holds an in-memory fake for tests. The production bus
// arrives with phase 1 lane 1.1.
package bus
