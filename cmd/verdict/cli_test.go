package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/frankbardon/verdict/pkg/dmn/vdj"
)

const loanModel = "../../examples/loan_approval/loan_approval.dmn"

// runCLI executes a subcommand with stdout captured, mirroring how the binary
// is actually invoked rather than calling the handler directly.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()

	cmd := &cli.Command{
		Name: "verdict",
		// Match main: an ExitCoder is returned, never acted on by cli itself.
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}},
		},
		Commands: []*cli.Command{cmdEval(), cmdAnalyze(), cmdExplain(), cmdConvert(),
			cmdTrace(), cmdSchema(), cmdServe(), cmdMCP()},
	}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := cmd.Run(context.Background(), append([]string{"verdict"}, args...))

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String(), runErr
}

func TestEvalCommand(t *testing.T) {
	out, err := runCLI(t, "eval", loanModel,
		"--bridge", "mock", "--agent-answer", "risk_tier_agent=low", "--quiet",
		"-d", `{"Applicant":{"age":41,"monthly_income":9000,"employment_years":12,
		        "credit_score":780,"existing_debt":400,"notes":"ok"},
		        "Loan":{"amount":120000,"term_months":240}}`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	var got struct {
		Outputs map[string]any `json:"outputs"`
		Trace   any            `json:"trace"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if got.Outputs["Routing"] != "auto-approve" {
		t.Errorf("Routing = %v, want auto-approve", got.Outputs["Routing"])
	}
	// stdout must stay machine-readable: diagnostics go to stderr, and the trace
	// only appears when asked for.
	if got.Trace != nil {
		t.Error("the trace was emitted without --trace")
	}
}

func TestEvalWithTraceIsPipeableIntoTrace(t *testing.T) {
	out, err := runCLI(t, "eval", loanModel,
		"--bridge", "mock", "--agent-answer", "risk_tier_agent=medium", "--quiet", "--trace",
		"-d", `{"Applicant":{"credit_score":700,"monthly_income":5000,"existing_debt":100,
		        "employment_years":3,"age":33,"notes":"n"},
		        "Loan":{"amount":50000,"term_months":120}}`)
	if err != nil {
		t.Fatalf("eval --trace: %v", err)
	}

	// Write the envelope to a file and render it, which is the documented
	// pipeline in a form a test can drive.
	tmp := t.TempDir() + "/trace.json"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	rendered, err := runCLI(t, "trace", tmp)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	for _, want := range []string{"Credit Rating", "decisionTable", "hit_policy", "Risk Tier"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered trace is missing %q:\n%s", want, rendered)
		}
	}
}

func TestEvalDecisionAndService(t *testing.T) {
	inputs := `{"Applicant":{"credit_score":700,"monthly_income":5000,"existing_debt":100,
	            "employment_years":3,"age":33,"notes":"n"},
	            "Loan":{"amount":50000,"term_months":120}}`

	out, err := runCLI(t, "eval", loanModel, "--quiet", "--decision", "credit_rating", "-d", inputs)
	if err != nil {
		t.Fatalf("eval --decision: %v", err)
	}
	if !strings.Contains(out, `"good"`) {
		t.Errorf("decision output = %s, want good", out)
	}

	out, err = runCLI(t, "eval", loanModel, "--quiet", "--service", "FastTrackDecisionService", "-d", inputs)
	if err != nil {
		t.Fatalf("eval --service: %v", err)
	}
	if !strings.Contains(out, "Affordability") {
		t.Errorf("service output = %s, want the service's declared outputs", out)
	}
}

func TestAnalyzeCommand(t *testing.T) {
	out, err := runCLI(t, "analyze", loanModel)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	for _, want := range []string{"DECISION", "Credit Rating", "OVERLAPS"} {
		if !strings.Contains(out, want) {
			t.Errorf("analyze output is missing %q:\n%s", want, out)
		}
	}

	jsonOut, err := runCLI(t, "analyze", loanModel, "--json")
	if err != nil {
		t.Fatalf("analyze --json: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatalf("--json output is not JSON: %v", err)
	}
	if report["model"] != "loan_approval" {
		t.Errorf("report model = %v", report["model"])
	}
}

func TestExplainCommand(t *testing.T) {
	index, err := runCLI(t, "explain", loanModel)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(index, "Decisions") || !strings.Contains(index, "routing") {
		t.Errorf("explain index is missing content:\n%s", index)
	}

	detail, err := runCLI(t, "explain", loanModel, "risk_tier")
	if err != nil {
		t.Fatalf("explain risk_tier: %v", err)
	}
	for _, want := range []string{"Inputs the agent sees", "Must answer", "On failure", "Prompt template"} {
		if !strings.Contains(detail, want) {
			t.Errorf("explain detail is missing %q:\n%s", want, detail)
		}
	}

	table, err := runCLI(t, "explain", loanModel, "credit_rating")
	if err != nil {
		t.Fatalf("explain credit_rating: %v", err)
	}
	// The rules must be rendered in full: an explanation that omits them is not
	// an explanation.
	if !strings.Contains(table, "credit_rating_r1") || !strings.Contains(table, ">= 740") {
		t.Errorf("explain did not render the table's rules:\n%s", table)
	}
}

func TestConvertRoundTrip(t *testing.T) {
	asJSON, err := runCLI(t, "convert", loanModel, "--to", "json")
	if err != nil {
		t.Fatalf("convert --to json: %v", err)
	}
	if !strings.Contains(asJSON, `"vdj"`) {
		t.Errorf("converted output is not VDJ:\n%s", asJSON[:min(len(asJSON), 200)])
	}

	tmp := t.TempDir() + "/model.vdj"
	if err := os.WriteFile(tmp, []byte(asJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	asXML, err := runCLI(t, "convert", tmp, "--to", "xml")
	if err != nil {
		t.Fatalf("convert back to xml: %v", err)
	}
	if !strings.Contains(asXML, "<definitions") || !strings.Contains(asXML, "agentDecision") {
		t.Errorf("round-tripped XML lost content:\n%s", asXML[:min(len(asXML), 400)])
	}
}

func TestUnknownModelIsAnError(t *testing.T) {
	if _, err := runCLI(t, "eval", "does-not-exist.dmn"); err == nil {
		t.Error("a missing model file was accepted")
	}
	if _, err := runCLI(t, "eval"); err == nil {
		t.Error("eval with no model argument was accepted")
	}
}

func TestNexusBridgeIsRefusedWithAPointer(t *testing.T) {
	// The CLI cannot construct the Nexus bridge — it lives in another module —
	// and must say so rather than silently evaluating agent nodes against
	// nothing.
	_, err := runCLI(t, "eval", loanModel, "--bridge", "nexus", "-d", "{}")
	if err == nil {
		t.Fatal("the nexus bridge was accepted by the CLI")
	}
	if !strings.Contains(err.Error(), "verdict/nexus") {
		t.Errorf("error does not point at the module that provides it: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestSchemaCommand checks that the CLI surface and the published file are the
// same bytes. If they diverge, a user validating locally and a user resolving
// the $id get different answers about the same document.
func TestSchemaCommand(t *testing.T) {
	out, err := runCLI(t, "schema")
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if got, want := strings.TrimSpace(out), strings.TrimSpace(string(vdj.BuildSchema())); got != want {
		t.Error("`verdict schema` does not emit vdj.BuildSchema() verbatim")
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the printed schema is not valid JSON: %v", err)
	}
	if doc["$id"] != vdj.SchemaID {
		t.Errorf("$id is %v, want %s", doc["$id"], vdj.SchemaID)
	}
}

// TestServingCommandsRefuseToStartWithNoModels covers the check that keeps a
// misconfigured deployment from looking healthy. A decision server that is up
// with an empty registry answers every call with a 404 the caller may not
// check, which is worse than failing to start.
func TestServingCommandsRefuseToStartWithNoModels(t *testing.T) {
	for _, name := range []string{"serve", "mcp"} {
		t.Run(name, func(t *testing.T) {
			_, err := runCLI(t, name)
			if err == nil {
				t.Fatal("the command started with no models loaded")
			}
			if !strings.Contains(err.Error(), "no models to serve") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestServingCommandsShareTheirModelFlags pins the two commands to one flag
// vocabulary. `verdict mcp` is `verdict serve` without a listener, and a user
// who learned one should not have to relearn the other.
func TestServingCommandsShareTheirModelFlags(t *testing.T) {
	shared := []string{"model", "mcp-allow-load", "model-root", "strict", "log-level",
		"bridge", "agent-endpoint", "agent-answer"}
	serve, mcp := flagNames(cmdServe()), flagNames(cmdMCP())
	for _, name := range shared {
		if !serve[name] {
			t.Errorf("serve is missing the %s flag", name)
		}
		if !mcp[name] {
			t.Errorf("mcp is missing the %s flag", name)
		}
	}
	// And the HTTP-only flags stay off the stdio command, where they would be
	// silently ignored.
	for _, name := range []string{"listen", "twirp-path", "no-mcp"} {
		if !serve[name] {
			t.Errorf("serve is missing the %s flag", name)
		}
		if mcp[name] {
			t.Errorf("mcp accepts %s, which it cannot honour", name)
		}
	}
}

func flagNames(cmd *cli.Command) map[string]bool {
	out := map[string]bool{}
	for _, f := range cmd.Flags {
		for _, n := range f.Names() {
			out[n] = true
		}
	}
	return out
}

// TestAnalyzeStrictIsARealGate covers a flag that is only useful if it changes
// what the command reports. `--strict` raises gap findings to errors; without
// it a CI pipeline that trusted the flag would go green on a table with a hole.
func TestAnalyzeStrictIsARealGate(t *testing.T) {
	const model = "testdata/shipping_gap.vdj"

	lenient, err := runCLI(t, "analyze", model)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !strings.Contains(lenient, "VERDICT_ANALYZE_001") {
		t.Fatalf("the example no longer reports a gap, so this test proves nothing:\n%s", lenient)
	}
	if strings.Contains(lenient, "[error] VERDICT_ANALYZE_001") {
		t.Error("a gap is an error without --strict")
	}

	strict, err := runCLI(t, "analyze", model, "--strict")
	if err == nil {
		t.Fatal("--strict did not fail on a model with an uncovered combination")
	}
	var coded cli.ExitCoder
	if !errors.As(err, &coded) || coded.ExitCode() != 2 {
		t.Errorf("--strict exited with %v, want status 2", err)
	}
	if !strings.Contains(strict, "[error] VERDICT_ANALYZE_001") {
		t.Errorf("--strict did not raise the gap to an error:\n%s", strict)
	}
	// The report itself must still be printed: a gate that fails without
	// saying what it found sends the reader back to run it again.
	if !strings.Contains(strict, "GAPS") {
		t.Error("--strict suppressed the report")
	}
}

// TestAnalyzeClipsProbesToTheDeclaredDomain is the regression for a false
// positive that made --strict unusable: the prober probes either side of every
// boundary, so a clause declaring [0..50] was probed at -1 and 51 and reported
// them as gaps. No rule can cover a value the modeller has said cannot occur,
// so a table that covered its whole declared domain could never be closed.
func TestAnalyzeClipsProbesToTheDeclaredDomain(t *testing.T) {
	out, err := runCLI(t, "analyze", "testdata/shipping_closed.vdj")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if strings.Contains(out, "gap:") || strings.Contains(out, "VERDICT_ANALYZE_001") {
		t.Errorf("a table covering its declared domain reported a gap:\n%s", out)
	}
	// The same model must also pass the gate, or the gate is unusable.
	if _, err := runCLI(t, "analyze", "testdata/shipping_closed.vdj", "--strict"); err != nil {
		t.Errorf("--strict failed on a model with no gaps: %v", err)
	}

	// And the clipping must not hide a real gap inside the domain.
	gappy, err := runCLI(t, "analyze", "testdata/shipping_gap.vdj")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !strings.Contains(gappy, `(50, "domestic")`) {
		t.Errorf("the in-domain gap at 50kg was not reported:\n%s", gappy)
	}
	if strings.Contains(gappy, `(0-1,`) {
		t.Errorf("an out-of-domain probe was still reported as a gap:\n%s", gappy)
	}
}

// TestSchemaValidateCatchesWhatTheLoaderTolerates is the point of shipping a
// validator in the binary. The reader accepts unknown fields on purpose — a
// newer VDJ version is likelier than a mistake — so a mistyped key loads as a
// silent omission. The schema is the only place it is caught.
func TestSchemaValidateCatchesWhatTheLoaderTolerates(t *testing.T) {
	raw, err := os.ReadFile("testdata/shipping_gap.vdj")
	if err != nil {
		t.Fatal(err)
	}
	typo := strings.Replace(string(raw), `"hit_policy"`, `"hit_polciy"`, 1)
	if typo == string(raw) {
		t.Fatal("the fixture no longer has a hit_policy to mistype")
	}
	path := filepath.Join(t.TempDir(), "typo.vdj")
	if err := os.WriteFile(path, []byte(typo), 0o644); err != nil {
		t.Fatal(err)
	}

	// The loader takes it, and quietly falls back to the default hit policy.
	if _, err := runCLI(t, "analyze", path); err != nil {
		t.Fatalf("the loader rejected a document it is supposed to tolerate: %v", err)
	}

	// The schema does not.
	_, err = runCLI(t, "schema", "--validate", path)
	if err == nil {
		t.Fatal("--validate accepted a document with an unknown field")
	}
	var coded cli.ExitCoder
	if !errors.As(err, &coded) || coded.ExitCode() != 2 {
		t.Errorf("--validate exited with %v, want status 2", err)
	}

	// And it accepts the file the typo came from.
	if _, err := runCLI(t, "schema", "--validate", "testdata/shipping_gap.vdj"); err != nil {
		t.Errorf("--validate rejected a valid document: %v", err)
	}
}
