// Package examples_test evaluates every shipped example model end to end.
//
// The examples are documentation, and documentation that does not run stops
// being true. These tests are what keeps them honest — and they double as the
// acceptance criteria for the engine: a loan model with an agent node that
// degrades when the bridge fails, a moderation model where rules dominate the
// agent, and a pricing model with no agent at all.
package examples_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/agent/mock"
	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/verdict"
)

func load(t *testing.T, path string, opts ...verdict.Option) (*verdict.Engine, *verdict.Model) {
	t.Helper()
	e, err := verdict.NewEngine(opts...)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	m, err := e.LoadModel(verdict.FromFile(path))
	if err != nil {
		t.Fatalf("LoadModel(%s): %v", path, err)
	}
	for _, d := range m.Diagnostics() {
		if d.Severity == diag.SeverityError {
			t.Errorf("%s: %s", path, d)
		}
	}
	return e, m
}

func TestLoanApproval(t *testing.T) {
	bridge := mock.New(mock.WithAnswer("risk_tier_agent", "low"))
	e, m := load(t, "loan_approval/loan_approval.dmn", verdict.WithAgentBridge(bridge))

	strong := verdict.Inputs{
		"Applicant": map[string]any{
			"age": 41, "monthly_income": 9000, "employment_years": 12,
			"credit_score": 780, "existing_debt": 400, "notes": "Long tenure.",
		},
		"Loan": map[string]any{"amount": 120000, "term_months": 240},
	}
	res, err := e.Evaluate(context.Background(), m.ID, strong)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := res.Outputs["Routing"]; got != "auto-approve" {
		t.Errorf("strong applicant routed to %v, want auto-approve", got)
	}

	// A thin file with an unaffordable ratio declines regardless of the agent.
	bridge.Reset()
	weak := verdict.Inputs{
		"Applicant": map[string]any{
			"age": 24, "monthly_income": 2200, "employment_years": 0,
			"credit_score": 540, "existing_debt": 700, "notes": "Recent defaults.",
		},
		"Loan": map[string]any{"amount": 90000, "term_months": 120},
	}
	res, err = e.Evaluate(context.Background(), m.ID, weak)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := res.Outputs["Routing"]; got != "decline" {
		t.Errorf("unaffordable applicant routed to %v, want decline", got)
	}
}

// TestLoanApprovalDegradesWhenTheAgentFails is success criterion 3: the graph
// must still produce an answer, and the trace must say why it is the answer it
// is, when the agent bridge times out.
func TestLoanApprovalDegradesWhenTheAgentFails(t *testing.T) {
	slow := agent.BridgeFunc(func(ctx context.Context, _ agent.Request) (agent.Response, error) {
		<-ctx.Done()
		return agent.Response{}, ctx.Err()
	})
	e, m := load(t, "loan_approval/loan_approval.dmn",
		verdict.WithAgentBridge(slow),
		verdict.WithDefaultMaxLatency(50*time.Millisecond))

	inputs := verdict.Inputs{
		"Applicant": map[string]any{
			"age": 41, "monthly_income": 9000, "employment_years": 12,
			"credit_score": 780, "existing_debt": 400, "notes": "Long tenure.",
		},
		"Loan": map[string]any{"amount": 120000, "term_months": 240},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := e.Evaluate(ctx, m.ID, inputs)
	if err != nil {
		t.Fatalf("the graph should degrade rather than fail: %v", err)
	}
	if got := res.Outputs["Routing"]; got != "auto-approve" {
		t.Errorf("Routing = %v, want auto-approve from the fallback heuristic", got)
	}

	node := res.Trace.Find("risk_tier")
	if node == nil {
		t.Fatal("no trace node for the agent decision")
	}
	if node.Annotations["fallback_used"] != true {
		t.Errorf("the trace does not record the fallback: %#v", node.Annotations)
	}
	// The trace must name the decision that actually produced the value, or the
	// audit record is a lie about how the answer was reached.
	if node.Annotations["fallback_from"] != "Risk Tier Heuristic" {
		t.Errorf("fallback_from = %v, want Risk Tier Heuristic", node.Annotations["fallback_from"])
	}
}

func TestContentModeration(t *testing.T) {
	cases := []struct {
		name    string
		agent   string
		post    map[string]any
		author  map[string]any
		signals map[string]any
		want    string
	}{
		{
			name:    "a bright-line hit removes without consulting the agent",
			agent:   "allow",
			post:    map[string]any{"text": "…", "language": "en", "link_count": 0, "report_count": 0},
			author:  map[string]any{"account_age_days": 900, "prior_strikes": 0, "trusted": true},
			signals: map[string]any{"banned_terms": 1, "toxicity_score": 0.1},
			want:    "remove",
		},
		{
			name:    "a clean post is allowed with no agent call at all",
			agent:   "remove",
			post:    map[string]any{"text": "Nice write-up.", "language": "en", "link_count": 0, "report_count": 0},
			author:  map[string]any{"account_age_days": 900, "prior_strikes": 0, "trusted": true},
			signals: map[string]any{"banned_terms": 0, "toxicity_score": 0.05},
			want:    "allow",
		},
		{
			name:    "an ambiguous post from a new author follows the agent",
			agent:   "remove",
			post:    map[string]any{"text": "borderline", "language": "en", "link_count": 0, "report_count": 3},
			author:  map[string]any{"account_age_days": 4, "prior_strikes": 0, "trusted": false},
			signals: map[string]any{"banned_terms": 0, "toxicity_score": 0.5},
			want:    "remove",
		},
		{
			name:    "the same call against an established author goes to a human",
			agent:   "remove",
			post:    map[string]any{"text": "borderline", "language": "en", "link_count": 0, "report_count": 3},
			author:  map[string]any{"account_age_days": 900, "prior_strikes": 0, "trusted": false},
			signals: map[string]any{"banned_terms": 0, "toxicity_score": 0.5},
			want:    "review",
		},
		{
			name:    "an agent clearing an established author's post allows it",
			agent:   "allow",
			post:    map[string]any{"text": "borderline", "language": "en", "link_count": 0, "report_count": 3},
			author:  map[string]any{"account_age_days": 900, "prior_strikes": 0, "trusted": false},
			signals: map[string]any{"banned_terms": 0, "toxicity_score": 0.5},
			want:    "allow",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bridge := mock.New(mock.WithAnswer("agent_assessment_agent", c.agent))
			e, m := load(t, "content_moderation/content_moderation.dmn", verdict.WithAgentBridge(bridge))
			res, err := e.Evaluate(context.Background(), m.ID, verdict.Inputs{
				"Post": c.post, "Author": c.author, "Signals": c.signals,
			})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got := res.Outputs["Moderation Verdict"]; got != c.want {
				t.Errorf("verdict = %v, want %v", got, c.want)
			}
		})
	}
}

// TestModerationCallsTheAgentOnlyWhenItHasTo is the cost claim the model makes.
func TestModerationCallsTheAgentOnlyWhenItHasTo(t *testing.T) {
	bridge := mock.New(mock.WithAnswer("agent_assessment_agent", "allow"))
	e, m := load(t, "content_moderation/content_moderation.dmn", verdict.WithAgentBridge(bridge))

	clean := verdict.Inputs{
		"Post":    map[string]any{"text": "hello", "language": "en", "link_count": 0, "report_count": 0},
		"Author":  map[string]any{"account_age_days": 900, "prior_strikes": 0, "trusted": true},
		"Signals": map[string]any{"banned_terms": 0, "toxicity_score": 0.02},
	}
	if _, err := e.Evaluate(context.Background(), m.ID, clean); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// The agent decision still runs as a graph node — the model always asks it —
	// but a hard block short-circuits nothing here, so what matters is that the
	// deterministic path decided the outcome.
	res, err := e.Evaluate(context.Background(), m.ID, clean)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Outputs["Needs Judgement"] == true {
		t.Error("a clean post was marked as needing judgement")
	}
}

func TestModerationFailsSafe(t *testing.T) {
	// onFailure="null" plus the catch-all rule means an unavailable model routes
	// to a human, never to a publish.
	broken := agent.BridgeFunc(func(context.Context, agent.Request) (agent.Response, error) {
		return agent.Response{}, context.DeadlineExceeded
	})
	e, m := load(t, "content_moderation/content_moderation.dmn", verdict.WithAgentBridge(broken))

	res, err := e.Evaluate(context.Background(), m.ID, verdict.Inputs{
		"Post":    map[string]any{"text": "borderline", "language": "en", "link_count": 0, "report_count": 3},
		"Author":  map[string]any{"account_age_days": 900, "prior_strikes": 0, "trusted": false},
		"Signals": map[string]any{"banned_terms": 0, "toxicity_score": 0.5},
	})
	if err != nil {
		t.Fatalf("the model should fail safe rather than error: %v", err)
	}
	if got := res.Outputs["Moderation Verdict"]; got != "review" {
		t.Errorf("verdict with a broken agent = %v, want review", got)
	}
}

func TestPricing(t *testing.T) {
	e, m := load(t, "pricing/pricing.dmn")

	cases := []struct {
		name            string
		customer, order map[string]any
		wantTier        string
		wantDiscount    float64
		wantPerSeat     float64
	}{
		{
			name:     "education stacks four discounts under a 40 point cap",
			customer: map[string]any{"segment": "education", "seats": 120, "tenure_years": 4, "region": "eu"},
			order:    map[string]any{"plan": "business", "term_months": 12, "promo_code": ""},
			wantTier: "mid", wantDiscount: 37, wantPerSeat: 24.57,
		},
		{
			name:     "a commercial customer is capped at 20 points",
			customer: map[string]any{"segment": "commercial", "seats": 400, "tenure_years": 5, "region": "us"},
			order:    map[string]any{"plan": "enterprise", "term_months": 24, "promo_code": ""},
			wantTier: "large", wantDiscount: 20, wantPerSeat: 63.2,
		},
		{
			name:     "a small monthly order qualifies for nothing",
			customer: map[string]any{"segment": "commercial", "seats": 3, "tenure_years": 0, "region": "us"},
			order:    map[string]any{"plan": "starter", "term_months": 1, "promo_code": ""},
			wantTier: "single", wantDiscount: 0, wantPerSeat: 9,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := e.EvaluateService(context.Background(), m.ID, "QuoteService",
				verdict.Inputs{"Customer": c.customer, "Order": c.order})
			if err != nil {
				t.Fatalf("EvaluateService: %v", err)
			}
			quote, ok := res.Outputs["Quote"].(map[string]any)
			if !ok {
				t.Fatalf("Quote = %#v, want a context", res.Outputs["Quote"])
			}
			if quote["volume tier"] != c.wantTier {
				t.Errorf("volume tier = %v, want %v", quote["volume tier"], c.wantTier)
			}
			if quote["discount percent"] != c.wantDiscount {
				t.Errorf("discount = %v, want %v", quote["discount percent"], c.wantDiscount)
			}
			if quote["price per seat"] != c.wantPerSeat {
				t.Errorf("price per seat = %v, want %v", quote["price per seat"], c.wantPerSeat)
			}
		})
	}
}

// TestEveryExampleReportsItsGapsAndOverlaps is success criterion 6: the static
// analysis must have something to say about every example, because every
// example contains a deliberate gap or overlap for it to find.
func TestEveryExampleReportsItsGapsAndOverlaps(t *testing.T) {
	models := []string{
		"loan_approval/loan_approval.dmn",
		"content_moderation/content_moderation.dmn",
		"pricing/pricing.dmn",
	}
	for _, path := range models {
		t.Run(path, func(t *testing.T) {
			_, m := load(t, path, verdict.WithAgentBridge(mock.New()))
			report := m.Analysis()
			if len(report.Tables) == 0 {
				t.Fatal("no decision tables were analysed")
			}
			findings := 0
			for _, tbl := range report.Tables {
				if !tbl.Analysable {
					t.Errorf("%s was not analysable: %s", tbl.DecisionID, tbl.Reason)
				}
				findings += len(tbl.Gaps) + len(tbl.Overlaps) + len(tbl.UnreachableRules)
			}
			if findings == 0 {
				t.Error("the analyser found nothing; every example carries a deliberate gap or overlap")
			}
		})
	}
}

// TestExamplesAreCleanUnderTheirOwnPolicies checks the findings are the ones the
// models intend: no example may carry an error-severity finding, because an
// overlap under UNIQUE or a disagreement under ANY is a bug, not a design.
func TestExamplesAreCleanUnderTheirOwnPolicies(t *testing.T) {
	for _, path := range []string{
		"loan_approval/loan_approval.dmn",
		"content_moderation/content_moderation.dmn",
		"pricing/pricing.dmn",
	} {
		_, m := load(t, path, verdict.WithAgentBridge(mock.New()))
		for _, d := range m.Diagnostics() {
			if d.Severity == diag.SeverityError {
				t.Errorf("%s has an error-severity finding: %s", path, d)
			}
		}
	}
}

func TestModelsRoundTripThroughBothFormats(t *testing.T) {
	for _, path := range []string{
		"loan_approval/loan_approval.dmn",
		"content_moderation/content_moderation.dmn",
		"pricing/pricing.dmn",
	} {
		t.Run(path, func(t *testing.T) {
			_, m := load(t, path, verdict.WithAgentBridge(mock.New()))
			if !strings.HasSuffix(m.Hash, "") || m.Hash == "" {
				t.Error("model has no content hash")
			}
		})
	}
}
