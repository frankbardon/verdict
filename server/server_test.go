package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankbardon/verdict/pkg/agent/mock"
	"github.com/frankbardon/verdict/pkg/verdict"
	"github.com/frankbardon/verdict/server"
	vtwirp "github.com/frankbardon/verdict/server/twirp"
)

const loanModel = "../examples/loan_approval/loan_approval.dmn"

func comfortableInputs() string {
	return `{"Applicant":{"age":41,"monthly_income":9000,"employment_years":12,` +
		`"credit_score":780,"existing_debt":400,"notes":"Long tenure."},` +
		`"Loan":{"amount":120000,"term_months":240}}`
}

func newTestServer(t *testing.T, opts ...func(*server.Options)) *httptest.Server {
	t.Helper()
	engine, err := verdict.NewEngine(
		verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := engine.LoadModel(verdict.FromFile(loanModel)); err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	o := server.Options{MCPEnabled: true, Version: "test"}
	for _, fn := range opts {
		fn(&o)
	}
	srv, err := server.New(engine, o)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// post issues a Twirp JSON call and decodes the response into out.
func post(t *testing.T, ts *httptest.Server, method, body string, out any) int {
	t.Helper()
	resp, err := http.Post(ts.URL+"/twirp/verdict.v1.Engine/"+method,
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && resp.StatusCode == http.StatusOK {
			t.Fatalf("decoding %s response: %v", method, err)
		}
	}
	return resp.StatusCode
}

func TestTwirpEvaluate(t *testing.T) {
	ts := newTestServer(t)

	var out struct {
		OutputsJson string `json:"outputs_json"`
		TraceID     string `json:"trace_id"`
		TraceJson   string `json:"trace_json"`
	}
	code := post(t, ts, "Evaluate",
		`{"model_id":"loan_approval","inputs_json":`+jsonString(comfortableInputs())+`}`, &out)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	var outputs map[string]any
	if err := json.Unmarshal([]byte(out.OutputsJson), &outputs); err != nil {
		t.Fatalf("outputs_json is not JSON: %v", err)
	}
	if outputs["Routing"] != "auto-approve" {
		t.Errorf("Routing = %#v, want auto-approve", outputs["Routing"])
	}
	// A trace is retained even when it was not requested inline, so a caller
	// can fetch it after the fact.
	if out.TraceJson != "" {
		t.Error("trace was returned inline without include_trace")
	}
	if out.TraceID == "" {
		t.Fatal("no trace id was returned")
	}

	var traceOut struct {
		TraceJson string `json:"trace_json"`
	}
	if code := post(t, ts, "GetTrace", `{"trace_id":"`+out.TraceID+`"}`, &traceOut); code != http.StatusOK {
		t.Fatalf("GetTrace status = %d, want 200", code)
	}
	if !strings.Contains(traceOut.TraceJson, "credit_rating") {
		t.Errorf("retained trace does not mention the decisions that fired: %s", traceOut.TraceJson)
	}
}

func TestTwirpEvaluateDecisionAndService(t *testing.T) {
	ts := newTestServer(t)

	var out struct {
		OutputsJson string `json:"outputs_json"`
	}
	post(t, ts, "EvaluateDecision",
		`{"model_id":"loan_approval","decision_id":"credit_rating","inputs_json":`+
			jsonString(comfortableInputs())+`}`, &out)
	if !strings.Contains(out.OutputsJson, "excellent") {
		t.Errorf("decision outputs = %s, want excellent", out.OutputsJson)
	}

	out.OutputsJson = ""
	post(t, ts, "EvaluateService",
		`{"model_id":"loan_approval","service_id":"FastTrackDecisionService","inputs_json":`+
			jsonString(comfortableInputs())+`}`, &out)
	if !strings.Contains(out.OutputsJson, "comfortable") {
		t.Errorf("service outputs = %s, want comfortable", out.OutputsJson)
	}
	// The service's encapsulated decision must not appear in its outputs.
	if strings.Contains(out.OutputsJson, "Repayment") {
		t.Errorf("service leaked its encapsulated decision: %s", out.OutputsJson)
	}
}

func TestTwirpAnalyzeAndExplain(t *testing.T) {
	ts := newTestServer(t)

	var analysis struct {
		ReportJson string `json:"report_json"`
	}
	post(t, ts, "Analyze", `{"model_id":"loan_approval"}`, &analysis)
	if !strings.Contains(analysis.ReportJson, "credit_rating") {
		t.Errorf("analysis report does not cover the model's tables: %s", analysis.ReportJson)
	}

	var explained struct {
		Text      string `json:"text"`
		SliceJson string `json:"slice_json"`
	}
	post(t, ts, "Explain", `{"model_id":"loan_approval","decision_id":"risk_tier"}`, &explained)
	if !strings.Contains(explained.Text, "Inputs the agent sees") {
		t.Errorf("explain text does not describe the agent decision:\n%s", explained.Text)
	}

	// With no decision named, Explain indexes the model.
	explained.Text = ""
	post(t, ts, "Explain", `{"model_id":"loan_approval"}`, &explained)
	if !strings.Contains(explained.Text, "Decisions") {
		t.Errorf("explain with no decision did not index the model:\n%s", explained.Text)
	}
}

func TestTwirpRejectsUnknownModel(t *testing.T) {
	ts := newTestServer(t)
	if code := post(t, ts, "Analyze", `{"model_id":"nope"}`, nil); code == http.StatusOK {
		t.Error("an unknown model returned 200")
	}
}

func TestLoadByPathIsRefusedWithoutARoot(t *testing.T) {
	ts := newTestServer(t)
	// A server with no model root must not read files a caller names, or it is
	// a file-disclosure primitive rather than a decision engine.
	if code := post(t, ts, "LoadModel", `{"path":"../../etc/passwd"}`, nil); code == http.StatusOK {
		t.Error("path loading was permitted with no model root configured")
	}
}

func TestLoadByPathIsConfinedToTheRoot(t *testing.T) {
	ts := newTestServer(t, func(o *server.Options) { o.ModelRoot = "../examples/loan_approval" })

	if code := post(t, ts, "LoadModel", `{"path":"loan_approval.dmn"}`, nil); code != http.StatusOK {
		t.Errorf("loading a model inside the root failed with %d", code)
	}
	for _, escape := range []string{"../../go.mod", "/etc/passwd", "../../../etc/passwd"} {
		if code := post(t, ts, "LoadModel", `{"path":"`+escape+`"}`, nil); code == http.StatusOK {
			t.Errorf("path %q escaped the model root", escape)
		}
	}
}

func TestLoadModelFromSource(t *testing.T) {
	ts := newTestServer(t)
	doc := `{"vdj":"1.0","id":"tiny","decisions":[{"id":"answer","name":"Answer",
	  "logic":{"kind":"literalExpression","text":"6 * 7"}}]}`

	var loaded struct {
		Model struct {
			ID string `json:"id"`
		} `json:"model"`
	}
	if code := post(t, ts, "LoadModel", `{"source":`+jsonString(doc)+`}`, &loaded); code != http.StatusOK {
		t.Fatalf("LoadModel status = %d, want 200", code)
	}
	if loaded.Model.ID != "tiny" {
		t.Fatalf("loaded model id = %q, want tiny", loaded.Model.ID)
	}

	var out struct {
		OutputsJson string `json:"outputs_json"`
	}
	post(t, ts, "Evaluate", `{"model_id":"tiny","inputs_json":"{}"}`, &out)
	if !strings.Contains(out.OutputsJson, "42") {
		t.Errorf("outputs = %s, want 42", out.OutputsJson)
	}
}

func TestHealthReportsLoadedModels(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	var health struct {
		Status string   `json:"status"`
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decoding health: %v", err)
	}
	if health.Status != "ok" || len(health.Models) != 1 || health.Models[0] != "loan_approval" {
		t.Errorf("health = %+v", health)
	}
}

func TestTraceStoreIsBounded(t *testing.T) {
	engine, err := verdict.NewEngine(verdict.WithAgentBridge(mock.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.LoadModel(verdict.FromFile(loanModel)); err != nil {
		t.Fatal(err)
	}
	rpc := vtwirp.NewServer(engine, vtwirp.WithTraceRetention(2))

	ctx := context.Background()
	var ids []string
	for i := 0; i < 4; i++ {
		res, err := rpc.Evaluate(ctx, &vtwirp.EvaluateRequest{
			ModelId: "loan_approval", InputsJson: comfortableInputs(),
		})
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		ids = append(ids, res.GetTraceId())
	}
	// The two oldest have aged out; the two newest are still fetchable. A trace
	// store that never evicts is a memory leak with a nice name.
	for _, id := range ids[:2] {
		if _, err := rpc.GetTrace(ctx, &vtwirp.GetTraceRequest{TraceId: id}); err == nil {
			t.Errorf("trace %s should have been evicted", id)
		}
	}
	for _, id := range ids[2:] {
		if _, err := rpc.GetTrace(ctx, &vtwirp.GetTraceRequest{TraceId: id}); err != nil {
			t.Errorf("trace %s should still be retained: %v", id, err)
		}
	}
}

// jsonString encodes s as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
