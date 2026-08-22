package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/frankbardon/verdict/pkg/agent/mock"
	"github.com/frankbardon/verdict/pkg/verdict"
	vmcp "github.com/frankbardon/verdict/server/mcp"
)

// TestMCPOverStdioServesTheSameSurface drives the stdio path end to end over an
// in-memory pipe.
//
// The transport is the only difference between `verdict serve`'s /mcp endpoint
// and `verdict mcp`; both build their server through newMCPServer. This test is
// what makes that a checked claim rather than a comment — an MCP client that
// sees different tools depending on how it reached Verdict is the kind of bug
// that only ever reproduces on someone else's machine.
func TestMCPOverStdioServesTheSameSurface(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	engine, err := verdict.NewEngine(
		verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := engine.LoadModel(verdict.FromFile("../examples/loan_approval/loan_approval.dmn")); err != nil {
		t.Fatalf("LoadModel: %v", err)
	}

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() {
		done <- serveMCP(ctx, engine, Options{MCPEnabled: true, Version: "test"}, serverT)
	}()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range tools.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{
		"verdict_list_models", "verdict_evaluate", "verdict_evaluate_decision",
		"verdict_explain", "verdict_analyze",
	} {
		if !got[want] {
			t.Errorf("tool %s is not served over stdio", want)
		}
	}
	// The load tool is off unless asked for, on stdio exactly as over HTTP.
	if got["verdict_load_model"] {
		t.Error("verdict_load_model is exposed without MCPAllowLoad")
	}

	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	uris := map[string]bool{}
	for _, r := range resources.Resources {
		uris[r.URI] = true
	}
	for _, want := range []string{vmcp.SchemaResourceURI, vmcp.SkillResourceURI} {
		if !uris[want] {
			t.Errorf("resource %s is not served over stdio", want)
		}
	}

	read, err := session.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: vmcp.SchemaResourceURI})
	if err != nil {
		t.Fatalf("resources/read: %v", err)
	}
	if len(read.Contents) != 1 {
		t.Fatalf("the schema resource returned %d contents, want 1", len(read.Contents))
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &schema); err != nil {
		t.Fatalf("the schema resource is not valid JSON: %v", err)
	}
	if schema["$id"] == nil {
		t.Error("the schema served over stdio has no $id")
	}

	// And a tool call actually reaches the engine, not just the catalogue.
	call, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "verdict_list_models"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if call.IsError {
		t.Fatalf("verdict_list_models reported an error: %+v", call.Content)
	}

	session.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serveMCP returned %v", err)
		}
	case <-ctx.Done():
		t.Error("serveMCP did not return after the client disconnected")
	}
}

// TestServeStdioRequiresAnEngine covers the guard, which matters more here than
// elsewhere: with no engine the process would otherwise sit on stdio looking
// healthy while answering nothing.
func TestServeStdioRequiresAnEngine(t *testing.T) {
	if err := ServeStdio(context.Background(), nil, Options{}); err == nil {
		t.Error("ServeStdio accepted a nil engine")
	}
}
