// Package gosdk mounts Verdict's MCP tool catalogue onto the official
// modelcontextprotocol/go-sdk server.
//
// The adapter is deliberately the only place the SDK is imported. Keeping the
// catalogue in pkg-level types means the tool surface can be tested, listed and
// documented without a transport, and a different SDK — or a different protocol
// entirely — is one adapter away.
package gosdk

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/frankbardon/verdict/pkg/verdict"
	vmcp "github.com/frankbardon/verdict/server/mcp"
)

// Register mounts every tool in the catalogue onto an SDK server.
func Register(srv *mcpsdk.Server, engine *verdict.Engine, cfg vmcp.Config) error {
	if srv == nil {
		return fmt.Errorf("verdict/mcp: nil server")
	}
	if engine == nil {
		return fmt.Errorf("verdict/mcp: nil engine")
	}
	for _, td := range vmcp.Tools(cfg) {
		if err := addTool(srv, engine, td); err != nil {
			return fmt.Errorf("verdict/mcp: registering %s: %w", td.Name, err)
		}
	}
	registerResources(srv)
	return nil
}

func addTool(srv *mcpsdk.Server, engine *verdict.Engine, td vmcp.ToolDescriptor) error {
	in, err := decodeSchema(td.InputSchema)
	if err != nil {
		return err
	}
	out, err := decodeSchema(td.OutputSchema)
	if err != nil {
		return err
	}

	tool := &mcpsdk.Tool{
		Name:         td.Name,
		Description:  td.Description,
		InputSchema:  in,
		OutputSchema: out,
	}
	invoke := td.Invoke
	mcpsdk.AddTool(srv, tool, func(ctx context.Context, _ *mcpsdk.CallToolRequest, args json.RawMessage) (*mcpsdk.CallToolResult, any, error) {
		result, err := invoke(ctx, engine, args)
		if err != nil {
			// A tool error is reported to the model as tool output rather than
			// as a protocol failure, so the agent can read what went wrong and
			// correct its next call instead of losing the turn.
			return &mcpsdk.CallToolResult{
				IsError: true,
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: err.Error()}},
			}, nil, nil
		}
		return nil, result, nil
	})
	return nil
}

// decodeSchema turns the catalogue's raw JSON schema into the SDK's type.
func decodeSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decoding schema: %w", err)
	}
	return &s, nil
}
