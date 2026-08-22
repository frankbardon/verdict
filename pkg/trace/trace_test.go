package trace_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/frankbardon/verdict/pkg/trace"
)

// fixedClock makes durations deterministic: every call advances by a known step,
// so a test can assert on recorded timings without sleeping.
func fixedClock() func() time.Time {
	base := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)
	n := 0
	return func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Millisecond)
	}
}

func TestRecorderBuildsATree(t *testing.T) {
	rec := trace.NewRecorder(trace.Full, nil, fixedClock())
	root, finishRoot := rec.Begin("model", "Model", "evaluation")

	child, finishChild := root.Child("credit_rating", "decisionTable")
	child.Annotate(trace.AnnHitPolicy, "U")
	child.Annotate(trace.AnnMatchedRules, []string{"r1"})
	if s, ok := child.(trace.InputSetter); ok {
		s.SetName("Credit Rating")
		s.SetInputs(map[string]any{"score": 780})
	}
	grand, finishGrand := child.Child("bkm", "invocation")
	finishGrand("inner", nil)
	_ = grand
	finishChild("excellent", nil)
	finishRoot(map[string]any{"Credit Rating": "excellent"}, nil)

	tr := rec.Trace("model", "hash", "Model", time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC))
	if tr == nil || tr.Root == nil {
		t.Fatal("no trace was produced")
	}
	if tr.ModelID != "model" || tr.ModelHash != "hash" {
		t.Errorf("trace identity = %q/%q", tr.ModelID, tr.ModelHash)
	}

	node := tr.Find("credit_rating")
	if node == nil {
		t.Fatal("Find did not locate the recorded node")
	}
	if node.DecisionName != "Credit Rating" {
		t.Errorf("node name = %q", node.DecisionName)
	}
	if node.Output != "excellent" {
		t.Errorf("node output = %#v", node.Output)
	}
	if node.Inputs["score"] != 780 {
		t.Errorf("node inputs = %#v", node.Inputs)
	}
	if node.Duration <= 0 {
		t.Error("node duration was not recorded")
	}
	if len(node.Children) != 1 || node.Children[0].DecisionID != "bkm" {
		t.Errorf("nested node was not recorded: %#v", node.Children)
	}

	visited := 0
	tr.Walk(func(*trace.Node) { visited++ })
	if visited != 3 {
		t.Errorf("Walk visited %d nodes, want 3", visited)
	}
}

func TestRedactionReplacesConfiguredBindings(t *testing.T) {
	rec := trace.NewRecorder(trace.Full, []string{"ssn"}, fixedClock())
	root, finish := rec.Begin("m", "M", "evaluation")
	if s, ok := root.(trace.InputSetter); ok {
		s.SetInputs(map[string]any{"ssn": "123-45-6789", "age": 41})
	}
	finish(nil, nil)

	tr := rec.Trace("m", "", "M", time.Now())
	if got := tr.Root.Inputs["ssn"]; got != trace.RedactedMarker {
		t.Errorf("redacted value = %#v, want the marker", got)
	}
	if got := tr.Root.Inputs["age"]; got != 41 {
		t.Errorf("an unredacted value was altered: %#v", got)
	}
}

func TestSummaryModeKeepsShapeAndDropsValues(t *testing.T) {
	rec := trace.NewRecorder(trace.Summary, nil, fixedClock())
	root, finish := rec.Begin("m", "M", "evaluation")
	child, finishChild := root.Child("d", "decisionTable")
	child.Annotate(trace.AnnHitPolicy, "U")      // structural: survives
	child.Annotate(trace.AnnInputValues, "data") // data: dropped
	if s, ok := child.(trace.InputSetter); ok {
		s.SetInputs(map[string]any{"secret": "value"})
	}
	finishChild("out", nil)
	finish(nil, nil)

	tr := rec.Trace("m", "", "M", time.Now())
	node := tr.Find("d")
	if node == nil {
		t.Fatal("summary mode dropped the node itself")
	}
	if node.Annotations[trace.AnnHitPolicy] != "U" {
		t.Error("summary mode dropped a structural annotation")
	}
	if _, present := node.Annotations[trace.AnnInputValues]; present {
		t.Error("summary mode kept a data annotation")
	}
	if node.Inputs != nil {
		t.Errorf("summary mode kept node inputs: %#v", node.Inputs)
	}
}

func TestOffModeRecordsNothing(t *testing.T) {
	rec := trace.NewRecorder(trace.Off, nil, fixedClock())
	root, finish := rec.Begin("m", "M", "evaluation")
	if root.Enabled() {
		t.Error("the writer reports itself enabled with tracing off")
	}
	child, finishChild := root.Child("d", "decisionTable")
	child.Annotate(trace.AnnHitPolicy, "U")
	finishChild(nil, nil)
	finish(nil, nil)

	if tr := rec.Trace("m", "", "M", time.Now()); tr != nil {
		t.Errorf("tracing is off but a trace was produced: %+v", tr)
	}
}

func TestErrorsAreRecorded(t *testing.T) {
	// The trace of a failed evaluation is the most valuable one there is, so a
	// failed node must still appear.
	rec := trace.NewRecorder(trace.Full, nil, fixedClock())
	root, finish := rec.Begin("m", "M", "evaluation")
	child, finishChild := root.Child("d", "decisionTable")
	finishChild(nil, errStub{})
	_ = child
	finish(nil, errStub{})

	tr := rec.Trace("m", "", "M", time.Now())
	if tr.Root.Error == "" {
		t.Error("the root's error was not recorded")
	}
	if node := tr.Find("d"); node == nil || node.Error == "" {
		t.Errorf("the failing node was not recorded: %#v", node)
	}
}

type errStub struct{}

func (errStub) Error() string { return "boom" }

func TestParseMode(t *testing.T) {
	for in, want := range map[string]trace.Mode{
		"":        trace.Full,
		"off":     trace.Off,
		"summary": trace.Summary,
		"full":    trace.Full,
	} {
		got, ok := trace.ParseMode(in)
		if !ok || got != want {
			t.Errorf("ParseMode(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	if _, ok := trace.ParseMode("verbose"); ok {
		t.Error("ParseMode accepted an unknown mode")
	}
}

func TestTraceIsJSONSerialisable(t *testing.T) {
	rec := trace.NewRecorder(trace.Full, nil, fixedClock())
	root, finish := rec.Begin("m", "M", "evaluation")
	_, finishChild := root.Child("d", "decisionTable")
	finishChild(map[string]any{"k": 1}, nil)
	finish(nil, nil)

	blob, err := json.Marshal(rec.Trace("m", "h", "M", time.Now()))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var back trace.Trace
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if back.Find("d") == nil {
		t.Errorf("round trip lost a node: %s", blob)
	}
}
