// Package mcp exposes a Verdict engine as MCP tools, so an agent can evaluate
// decisions, read a decision's rules, and check a model for gaps and overlaps
// without a human in the loop.
//
// The catalogue is SDK-agnostic: a ToolDescriptor carries a name, reflected
// input and output schemas, and a type-erased Invoke. The go-sdk adapter in
// gosdk/ mounts them; anything else can too. Keeping the catalogue free of the
// SDK means the tool surface is testable without a transport.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/frankbardon/verdict/pkg/verdict"
)

// Default server identity, used when a Config leaves them unset.
const (
	DefaultServerName = "verdict"
	DefaultVersion    = "1.0.0"
)

// Config carries the server-construction parameters for the tool catalogue.
type Config struct {
	ServerName string
	Version    string
	// AllowLoad exposes the verdict_load tool, which registers a model sent by
	// the caller. Off by default: an agent that can load arbitrary models into
	// a shared engine can also shadow the ones an operator deployed.
	AllowLoad bool
}

// ToolDescriptor describes one Verdict MCP tool, independent of transport and
// SDK. Invoke is type-erased: it unmarshals raw JSON arguments into the tool's
// typed input, calls the typed handler, and returns the typed output.
type ToolDescriptor struct {
	Name         string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	Invoke       func(ctx context.Context, e *verdict.Engine, raw json.RawMessage) (any, error)
}

// Reflected schemas, computed once at init from struct tags. Never
// hand-written: a hand-written schema drifts from the struct on the first
// field added.
var (
	evaluateInputSchema  = reflectSchema[EvaluateInput]()
	evaluateOutputSchema = reflectSchema[EvaluateOutput]()

	evaluateDecisionInputSchema = reflectSchema[EvaluateDecisionInput]()

	explainInputSchema  = reflectSchema[ExplainInput]()
	explainOutputSchema = reflectSchema[ExplainOutput]()

	analyzeInputSchema  = reflectSchema[AnalyzeInput]()
	analyzeOutputSchema = reflectSchema[AnalyzeOutput]()

	listInputSchema  = reflectSchema[ListInput]()
	listOutputSchema = reflectSchema[ListOutput]()

	loadInputSchema  = reflectSchema[LoadInput]()
	loadOutputSchema = reflectSchema[LoadOutput]()
)

func reflectSchema[T any]() json.RawMessage {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("verdict/mcp: reflecting schema for %T: %v", *new(T), err))
	}
	raw, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("verdict/mcp: encoding schema for %T: %v", *new(T), err))
	}
	return raw
}

// Tools returns the catalogue.
func Tools(cfg Config) []ToolDescriptor {
	tools := []ToolDescriptor{
		{
			Name: "verdict_list_models",
			Description: "List the decision models this engine has loaded, with their versions, " +
				"the decisions and decision services each offers, and which decisions are " +
				"top-level outputs. Start here when you do not know what is available.",
			InputSchema:  listInputSchema,
			OutputSchema: listOutputSchema,
			Invoke:       erase(ListModels),
		},
		{
			Name: "verdict_evaluate",
			Description: "Evaluate a decision model's top-level decisions against a set of inputs " +
				"and return the outputs with an execution trace. Use verdict_explain first to " +
				"learn which inputs the model requires and what they are named.",
			InputSchema:  evaluateInputSchema,
			OutputSchema: evaluateOutputSchema,
			Invoke:       erase(Evaluate),
		},
		{
			Name: "verdict_evaluate_decision",
			Description: "Evaluate one decision (or one decision service) and its dependencies, " +
				"rather than the whole model. Cheaper than verdict_evaluate when you need a " +
				"single answer, and the trace shows exactly which rules fired.",
			InputSchema:  evaluateDecisionInputSchema,
			OutputSchema: evaluateOutputSchema,
			Invoke:       erase(EvaluateDecision),
		},
		{
			Name: "verdict_explain",
			Description: "Describe a decision: the inputs it needs, the decisions it builds on, " +
				"and its logic in full — every rule of a decision table, or the bindings, " +
				"output type and failure policy of an agent decision. With no decision named, " +
				"lists the model's decisions and services.",
			InputSchema:  explainInputSchema,
			OutputSchema: explainOutputSchema,
			Invoke:       erase(Explain),
		},
		{
			Name: "verdict_analyze",
			Description: "Report the gaps (input combinations no rule covers) and overlaps " +
				"(combinations several rules cover) in a model's decision tables, plus any " +
				"rule an earlier rule makes unreachable. Use it to check a model is complete " +
				"and unambiguous before trusting its answers.",
			InputSchema:  analyzeInputSchema,
			OutputSchema: analyzeOutputSchema,
			Invoke:       erase(Analyze),
		},
	}
	if cfg.AllowLoad {
		tools = append(tools, ToolDescriptor{
			Name: "verdict_load_model",
			Description: "Register a decision model with the engine from a DMN XML or Verdict " +
				"Decision JSON document. Loading the same document twice is idempotent.",
			InputSchema:  loadInputSchema,
			OutputSchema: loadOutputSchema,
			Invoke:       erase(LoadModel),
		})
	}
	return tools
}

// erase adapts a typed handler to the type-erased Invoke signature.
func erase[In any, Out any](fn func(context.Context, *verdict.Engine, In) (Out, error)) func(context.Context, *verdict.Engine, json.RawMessage) (any, error) {
	return func(ctx context.Context, e *verdict.Engine, raw json.RawMessage) (any, error) {
		var in In
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("arguments do not match this tool's schema: %w", err)
			}
		}
		return fn(ctx, e, in)
	}
}
