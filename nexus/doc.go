// Package nexus is the Nexus-backed AgentBridge for Verdict: it lets an
// `agentDecision` node in a decision graph be answered by a Nexus session.
//
// # Why this is a separate module
//
// Verdict's core has no dependency on Nexus, and this package is where that
// boundary is enforced rather than merely intended. It is a distinct Go module
// (`github.com/frankbardon/verdict/nexus`), so a service that imports
// `github.com/frankbardon/verdict` cannot transitively acquire Nexus, its
// provider SDKs, or its plugin surface. A downstream system that wants both
// imports both, explicitly.
//
// The dependency is deliberately one-directional: this module imports Verdict
// and Nexus; neither imports this one.
//
// # The two directions
//
// Verdict and Nexus compose in both directions, and this module supplies both
// halves.
//
//	Bridge — Nexus answers Verdict.
//	  A Verdict engine running anywhere routes its agentDecision nodes into
//	  Nexus. Each invocation is a real agent run with a workspace, tools and an
//	  event-bus record, not a bare model call, and the decision trace records
//	  the session reference so the run stays replayable afterwards.
//
//	Plugin — Verdict answers Nexus.
//	  The `nexus.decision.verdict` plugin gives a Nexus agent deterministic
//	  decision-making as a first-class capability: it loads models at boot,
//	  listens for `decision.requested` on the bus, evaluates, and emits
//	  `decision.completed` with the outputs and the trace.
//
// Wiring both at once is the interesting case: an agent asks Verdict for a
// classification, one of Verdict's nodes asks a fresh Nexus session for a
// judgement, and the answer flows back. The recursion terminates because the
// DRG is acyclic and the bridge caps its own depth.
package nexus
