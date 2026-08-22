# Security

## Reporting a vulnerability

Report privately through GitHub's security advisory form on this repository, or
by email to the maintainer. Please do not open a public issue for a
vulnerability.

Include what you did, what happened, and what you expected. A model document or
input payload that reproduces the problem is the most useful thing you can send.

## Threat model

Verdict evaluates decision models. The properties it is expected to hold:

**A model is code.** Loading a model means agreeing to run its FEEL expressions.
Verdict does not sandbox them — they cannot reach the filesystem, the network or
the host, because FEEL has no facility for any of that, but a hostile model can
still consume CPU. Treat a decision model with the same care as a code
dependency, and do not load one from an untrusted source.

**Inputs are data.** Caller-supplied inputs are values, never expressions. There
is no path by which an input becomes evaluated code.

**Agent bindings are the encapsulation boundary.** An `agentDecision` sees only
the values its declared `inputBinding` expressions produce — never the model
context. Any change that widens what reaches `agent.Request.Inputs` is a
security change and is treated as one.

**Traces carry data.** A full trace records every node's inputs and outputs by
design. Use `redact_inputs` for anything that must not be persisted, or run in
`summary` mode, which keeps the shape of a decision without the data that flowed
through it.

## Server

`verdict serve` is designed to run inside a trust boundary and has no authentication
of its own. Put it behind whatever your environment already uses.

Two defaults exist because the alternative is a footgun:

- **`LoadModel` by path is refused** unless `--model-root` is set, and confined
  to that directory when it is. A server that reads any path a caller names is a
  file-disclosure primitive.
- **The MCP model-loading tool is off** unless `--mcp-allow-load` is passed. An
  agent that can load models into a shared engine can shadow the ones an
  operator deployed.

Request bodies are not logged. A decision request is the caller's data, and
logging it by default would quietly turn the server into a data store.

## Supported versions

The most recent minor release receives security fixes.
