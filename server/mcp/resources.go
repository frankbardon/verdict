package mcp

import (
	"context"
	"fmt"

	"github.com/frankbardon/verdict/pkg/dmn/vdj"
	"github.com/frankbardon/verdict/skill"
)

// URIScheme is the URI scheme Verdict's MCP resources live under.
const URIScheme = "verdict://"

const (
	// SchemaResourceURI serves the VDJ JSON Schema. It is the same document
	// `verdict schema` prints and the documentation site publishes, so an agent
	// that reads it in-session and a human validating a file locally are held
	// to the same contract.
	SchemaResourceURI = URIScheme + "schema"

	// SkillResourceURI serves the embedded skill pack: how to drive Verdict,
	// written for a model rather than for a reader of the manual.
	SkillResourceURI = URIScheme + "skill"
)

// ResourceDescriptor describes one MCP resource, independent of transport and
// SDK, exactly as ToolDescriptor does for tools. Read returns the body.
type ResourceDescriptor struct {
	URI         string
	Name        string
	Description string
	MIMEType    string
	Read        func(ctx context.Context) ([]byte, error)
}

// Resources returns the resource catalogue.
//
// Both entries are static and model-independent: they describe the format and
// the engine, not any loaded model, so they are safe to expose regardless of
// what the server has been given to evaluate.
func Resources() []ResourceDescriptor {
	return []ResourceDescriptor{
		{
			URI:         SchemaResourceURI,
			Name:        "vdj-schema",
			Description: "JSON Schema (draft 2020-12) for Verdict Decision JSON, the JSON projection of a DMN model",
			MIMEType:    "application/json",
			Read: func(context.Context) ([]byte, error) {
				return vdj.BuildSchema(), nil
			},
		},
		{
			URI:         SkillResourceURI,
			Name:        "verdict-skill",
			Description: "The embedded Verdict skill pack: how to write, evaluate and analyse a decision model",
			MIMEType:    "text/markdown",
			Read: func(context.Context) ([]byte, error) {
				return []byte(skill.Markdown()), nil
			},
		},
	}
}

// Resource returns the descriptor for a URI.
func Resource(uri string) (ResourceDescriptor, error) {
	for _, r := range Resources() {
		if r.URI == uri {
			return r, nil
		}
	}
	return ResourceDescriptor{}, fmt.Errorf("verdict/mcp: no resource at %q", uri)
}
