package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankbardon/verdict/pkg/agent"
	agenthttp "github.com/frankbardon/verdict/pkg/agent/http"
	"github.com/frankbardon/verdict/pkg/dmn/model"
)

func TestBridgePostsTheDeclaredContract(t *testing.T) {
	var got agenthttp.RequestBody
	var sawHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = r.Header.Get("X-Api-Key")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("request body is not the documented envelope: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agenthttp.ResponseBody{
			Value: "medium", SessionRef: "svc-1",
			InputTokens: 40, OutputTokens: 2, Model: "test-model",
		})
	}))
	defer srv.Close()

	b := agenthttp.New(srv.URL, agenthttp.WithHeader("X-Api-Key", "secret"))
	resp, err := b.Invoke(context.Background(), agent.Request{
		DecisionID:   "risk_tier",
		DecisionName: "Risk Tier",
		Prompt:       "Classify.",
		Inputs:       map[string]any{"score": 700},
		OutputType:   model.TypeSpec{TypeRef: "string", Enumeration: []string{"low", "medium", "high"}},
		SessionHint:  "underwriting",
		Attempt:      1,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if resp.Value != "medium" || resp.SessionRef != "svc-1" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Tokens.Total != 42 || resp.Tokens.Model != "test-model" {
		t.Errorf("token accounting = %+v", resp.Tokens)
	}
	if sawHeader != "secret" {
		t.Errorf("configured header did not reach the endpoint: %q", sawHeader)
	}

	// The endpoint must receive everything it needs to constrain the model:
	// the prompt, the bound inputs, and the declared answer shape.
	if got.DecisionID != "risk_tier" || got.DecisionName != "Risk Tier" {
		t.Errorf("decision identity = %q/%q", got.DecisionID, got.DecisionName)
	}
	if got.Attempt != 1 || got.SessionHint != "underwriting" {
		t.Errorf("attempt/hint = %d/%q", got.Attempt, got.SessionHint)
	}
	if len(got.OutputType.Enumeration) != 3 {
		t.Errorf("declared output type did not cross the wire: %+v", got.OutputType)
	}
	if got.Inputs["score"] != 700.0 {
		t.Errorf("inputs = %#v", got.Inputs)
	}
}

func TestBridgeReportsTransportAndSemanticFailures(t *testing.T) {
	t.Run("a non-2xx status is a failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream is down"))
		}))
		defer srv.Close()
		if _, err := agenthttp.New(srv.URL).Invoke(context.Background(), agent.Request{}); err == nil {
			t.Fatal("a 502 was treated as success")
		} else if !strings.Contains(err.Error(), "upstream is down") {
			t.Errorf("error does not carry the endpoint's explanation: %v", err)
		}
	})

	t.Run("an error field on a 200 is a failure", func(t *testing.T) {
		// Some gateways report semantic failure with a 200 body. Treating it as
		// success would let a decision bind an error message as its value.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(agenthttp.ResponseBody{Error: "model refused"})
		}))
		defer srv.Close()
		if _, err := agenthttp.New(srv.URL).Invoke(context.Background(), agent.Request{}); err == nil {
			t.Fatal("an error body with a 200 status was treated as success")
		}
	})

	t.Run("a cancelled context aborts the call", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := agenthttp.New(srv.URL).Invoke(ctx, agent.Request{}); err == nil {
			t.Fatal("a cancelled context did not abort the call")
		}
	})

	t.Run("a non-JSON body is a failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>not json</html>"))
		}))
		defer srv.Close()
		if _, err := agenthttp.New(srv.URL).Invoke(context.Background(), agent.Request{}); err == nil {
			t.Fatal("an HTML body was decoded as a response")
		}
	})
}

func TestStructuredOutputTypeIsProjected(t *testing.T) {
	var got agenthttp.RequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_ = json.NewEncoder(w).Encode(agenthttp.ResponseBody{
			Value: map[string]any{"tier": "low", "confidence": 0.8},
		})
	}))
	defer srv.Close()

	resp, err := agenthttp.New(srv.URL).Invoke(context.Background(), agent.Request{
		OutputType: model.TypeSpec{Components: map[string]model.TypeSpec{
			"tier":       {Enumeration: []string{"low", "high"}},
			"confidence": {TypeRef: "number"},
		}},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(got.OutputType.Components) != 2 {
		t.Errorf("component types did not cross the wire: %+v", got.OutputType)
	}
	obj, ok := resp.Value.(map[string]any)
	if !ok || obj["tier"] != "low" {
		t.Errorf("structured value = %#v", resp.Value)
	}
}
