package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/feel"
	"github.com/frankbardon/verdict/pkg/trace"
)

// prepareAgent parses an agent decision's prompt template, input bindings and
// validator at load time, so a malformed agent node is a deploy-time failure
// rather than a production surprise.
func (p *Program) prepareAgent(ds *diag.Set, elemID, elemName string, a *model.AgentDecision) {
	tmpl, err := template.New(elemID).Option("missingkey=zero").Parse(a.PromptTemplate)
	if err != nil {
		ds.Add(diag.Errorf(diag.CodeBadTemplate,
			"agent decision prompt template does not parse: %v", err).At(elemID, elemName).In(p.Defs.ID))
	} else {
		p.templates[a] = tmpl
	}
	for _, b := range a.Bindings {
		p.compileText(ds, elemID, elemName, b.FEEL)
	}
	if a.Validator != "" {
		if x := p.compileText(ds, elemID, elemName, a.Validator); x != nil {
			p.agentValidators[a] = x
		}
	}
	if a.Policy.OnFailure == model.FailFallback && a.Policy.FallbackDecision != "" {
		if _, ok := p.Graph.Decision(a.Policy.FallbackDecision); !ok {
			ds.Add(diag.Errorf(diag.CodeDanglingRequirement,
				"agent decision names fallbackDecision %q, which this model does not define",
				a.Policy.FallbackDecision).At(elemID, elemName).In(p.Defs.ID))
		}
	}
}

// evalAgent evaluates an agentDecision: bind inputs, render the prompt, call
// the bridge under the declared latency and retry budget, coerce and validate
// the answer, and apply the failure policy if anything went wrong.
func (p *Program) evalAgent(ec *Context, a *model.AgentDecision) (any, error) {
	value, err := p.invokeAgent(ec, a)
	if err == nil {
		return value, nil
	}

	switch a.Policy.OnFailure {
	case model.FailNull:
		ec.diagnose(diag.Warnf(diag.CodeAgentFailure,
			"agent decision failed (%v); onFailure=null so the decision is null", err).At(a.ExprID(), ""))
		ec.trace.Annotate(trace.AnnError, err.Error())
		return feel.Null, nil
	case model.FailFallback:
		fallback, ok := p.Graph.Decision(a.Policy.FallbackDecision)
		if !ok {
			return nil, diag.Errorf(diag.CodeAgentFailure,
				"agent decision failed (%v) and its fallback %q does not exist",
				err, a.Policy.FallbackDecision).At(a.ExprID(), "")
		}
		ec.diagnose(diag.Warnf(diag.CodeFallbackUsed,
			"agent decision failed (%v); evaluating fallback decision %q instead",
			err, fallback.Name).At(a.ExprID(), ""))
		ec.trace.Annotate(trace.AnnError, err.Error())
		ec.trace.Annotate(trace.AnnFallbackUsed, true)
		ec.trace.Annotate(trace.AnnFallbackFrom, fallback.Name)

		child, finish := ec.trace.Child(fallback.ID, kindOf(fallback.Logic))
		if s, ok := child.(trace.InputSetter); ok {
			s.SetName(fallback.Name)
		}
		v, ferr := p.evalExpression(ec.withTrace(child), fallback.Logic)
		finish(feel.ToGo(v), ferr)
		if ferr != nil {
			return nil, diag.Errorf(diag.CodeAgentFailure,
				"agent decision failed (%v) and its fallback %q also failed: %v",
				err, fallback.Name, ferr).At(a.ExprID(), "")
		}
		return v, nil
	default:
		return nil, err
	}
}

// invokeAgent performs the bridge call with retries, enforcing the latency
// budget across the whole attempt sequence.
func (p *Program) invokeAgent(ec *Context, a *model.AgentDecision) (any, error) {
	if p.cfg.Bridge == nil {
		return nil, diag.Errorf(diag.CodeAgentFailure,
			"model contains an agent decision but no agent bridge is registered").At(a.ExprID(), "")
	}

	inputs, err := p.bindAgentInputs(ec, a)
	if err != nil {
		return nil, err
	}
	prompt, err := p.renderPrompt(a, inputs)
	if err != nil {
		return nil, err
	}
	if ec.trace.Enabled() {
		ec.trace.Annotate(trace.AnnPrompt, prompt)
		if s, ok := ec.trace.(trace.InputSetter); ok {
			s.SetInputs(inputs)
		}
	}

	budget := a.Policy.MaxLatency
	if budget <= 0 {
		budget = p.cfg.DefaultMaxLatency
	}
	callCtx := ec.ctx
	var cancel context.CancelFunc
	if budget > 0 {
		callCtx, cancel = context.WithTimeout(ec.ctx, budget)
		defer cancel()
	}

	req := agent.Request{
		DecisionID:  a.ExprID(),
		Prompt:      prompt,
		Inputs:      inputs,
		OutputType:  a.OutputType,
		Trace:       ec.trace,
		SessionHint: a.Policy.SessionHint,
	}
	if name, ok := ec.vars[currentDecisionNameKey].(string); ok {
		req.DecisionName = name
	}

	var lastErr error
	attempts := a.Policy.MaxRetries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		if err := callCtx.Err(); err != nil {
			lastErr = wrapAgentTimeout(a, budget, err, attempt)
			break
		}
		req.Attempt = attempt
		resp, err := p.cfg.Bridge.Invoke(callCtx, req)
		if err != nil {
			lastErr = err
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				lastErr = wrapAgentTimeout(a, budget, err, attempt+1)
				break
			}
			continue
		}
		ec.trace.Annotate(trace.AnnAttempts, attempt+1)
		if resp.SessionRef != "" {
			ec.trace.Annotate(trace.AnnSessionRef, resp.SessionRef)
		}
		if resp.Tokens.Total > 0 || resp.Tokens.Input > 0 || resp.Tokens.Output > 0 {
			ec.trace.Annotate(trace.AnnTokens, resp.Tokens)
		}

		value, err := coerceToType(resp.Value, a.OutputType)
		if err != nil {
			lastErr = diag.Errorf(diag.CodeTypeViolation,
				"agent answered with a value that does not match the declared output type: %v", err).At(a.ExprID(), "")
			continue
		}
		if err := p.validateAgentValue(ec, a, value); err != nil {
			lastErr = err
			continue
		}
		return value, nil
	}

	if lastErr == nil {
		lastErr = diag.Errorf(diag.CodeAgentFailure, "agent decision produced no answer").At(a.ExprID(), "")
	}
	ec.trace.Annotate(trace.AnnAttempts, attempts)
	return nil, lastErr
}

func wrapAgentTimeout(a *model.AgentDecision, budget time.Duration, err error, attempts int) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return diag.Errorf(diag.CodeAgentTimeout,
		"agent decision exceeded its %s latency budget after %d attempt(s)", budget, attempts).At(a.ExprID(), "")
}

// currentDecisionNameKey is the binding under which the engine makes the name
// of the decision being evaluated available to the agent plumbing. It is not a
// legal FEEL name, so a model cannot collide with it.
const currentDecisionNameKey = "__verdict_decision_name"

// bindAgentInputs evaluates the declared bindings. The agent sees these values
// and nothing else — the same encapsulation discipline DMN applies to a BKM's
// parameters.
func (p *Program) bindAgentInputs(ec *Context, a *model.AgentDecision) (map[string]any, error) {
	out := make(map[string]any, len(a.Bindings))
	for _, b := range a.Bindings {
		x, err := p.cfg.FEEL.Compile(b.FEEL)
		if err != nil {
			return nil, diag.Errorf(diag.CodeExpressionError,
				"agent input binding %q: %v", b.Name, err).At(a.ExprID(), "")
		}
		v, err := ec.eval(x)
		if err != nil {
			return nil, diag.Errorf(diag.CodeExpressionError,
				"agent input binding %q: %v", b.Name, err).At(a.ExprID(), "")
		}
		out[b.Name] = feel.ToGo(v)
	}
	return out, nil
}

func (p *Program) renderPrompt(a *model.AgentDecision, inputs map[string]any) (string, error) {
	tmpl := p.templates[a]
	if tmpl == nil {
		// The template failed to parse at load time; the diagnostic is already
		// recorded, and sending an unrendered template to an agent would be worse
		// than failing here.
		return "", diag.Errorf(diag.CodeBadTemplate,
			"agent decision prompt template did not compile").At(a.ExprID(), "")
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData(inputs)); err != nil {
		return "", diag.Errorf(diag.CodeBadTemplate,
			"rendering agent prompt: %v", err).At(a.ExprID(), "")
	}
	return buf.String(), nil
}

// templateData renders values the way a prompt wants to read them: structured
// values as indented JSON rather than as Go's `map[k:v]` spelling, which models
// parse poorly.
func templateData(inputs map[string]any) map[string]any {
	out := make(map[string]any, len(inputs))
	for k, v := range inputs {
		switch v.(type) {
		case map[string]any, []any:
			if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				out[k] = string(b)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// validateAgentValue runs the optional FEEL validator with the coerced answer
// bound to `value`.
func (p *Program) validateAgentValue(ec *Context, a *model.AgentDecision, value any) error {
	x := p.agentValidators[a]
	if x == nil {
		return nil
	}
	v, err := p.cfg.FEEL.Eval(x, mergeVars(ec.vars, map[string]any{"value": feel.FromGo(value)}))
	if err != nil {
		return diag.Errorf(diag.CodeAgentValidation,
			"agent output validator failed to evaluate: %v", err).At(a.ExprID(), "")
	}
	ok, isBool := v.(bool)
	if !isBool || !ok {
		return diag.Errorf(diag.CodeAgentValidation,
			"agent answered %v, which the validator %q rejected", value, a.Validator).At(a.ExprID(), "")
	}
	return nil
}

func mergeVars(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// coerceToType turns a bridge's plain-Go answer into a FEEL value conforming to
// the declared type, or reports why it cannot.
//
// Coercion is deliberately generous about *representation* and strict about
// *meaning*: a model that answers `"42"` for a number is accepted, because the
// text is unambiguous; a model that answers `"high risk"` for an enumeration of
// low/medium/high is rejected, because guessing would be inventing a decision.
func coerceToType(v any, spec model.TypeSpec) (any, error) {
	if spec.Collection {
		items, ok := toSlice(v)
		if !ok {
			return nil, fmt.Errorf("expected a list, got %T", v)
		}
		elem := spec
		elem.Collection = false
		out := make([]any, 0, len(items))
		for i, item := range items {
			c, err := coerceToType(item, elem)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i+1, err)
			}
			out = append(out, c)
		}
		return out, nil
	}

	if len(spec.Components) > 0 {
		obj, ok := toMap(v)
		if !ok {
			return nil, fmt.Errorf("expected an object with fields %s, got %T", fieldNames(spec.Components), v)
		}
		out := make(map[string]any, len(spec.Components))
		for name, comp := range spec.Components {
			raw, present := obj[name]
			if !present {
				return nil, fmt.Errorf("missing field %q", name)
			}
			c, err := coerceToType(raw, comp)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", name, err)
			}
			out[name] = c
		}
		return out, nil
	}

	if len(spec.Enumeration) > 0 {
		s, ok := v.(string)
		if !ok {
			s = fmt.Sprint(feel.ToGo(feel.FromGo(v)))
		}
		s = strings.TrimSpace(s)
		for _, allowed := range spec.Enumeration {
			if s == allowed {
				return allowed, nil
			}
		}
		// A single case-insensitive retry: models routinely capitalise. Anything
		// looser would be guessing.
		for _, allowed := range spec.Enumeration {
			if strings.EqualFold(s, allowed) {
				return allowed, nil
			}
		}
		return nil, fmt.Errorf("%q is not one of %s", s, strings.Join(spec.Enumeration, ", "))
	}

	switch baseTypeName(spec.TypeRef) {
	case "":
		return feel.FromGo(v), nil
	case model.TypeNumber:
		return coerceNumber(v)
	case model.TypeString:
		if s, ok := v.(string); ok {
			return s, nil
		}
		return nil, fmt.Errorf("expected a string, got %T", v)
	case model.TypeBoolean:
		return coerceBool(v)
	case model.TypeDate, model.TypeTime, model.TypeDateTime,
		model.TypeDayTimeDur, model.TypeYearMonth:
		return coerceTemporal(v, baseTypeName(spec.TypeRef))
	default:
		// A user-defined item definition: accept the value as-is and leave
		// enforcement to the optional validator, which is the escape hatch the
		// model author has for anything the type spec cannot express.
		return feel.FromGo(v), nil
	}
}

func baseTypeName(typeRef string) string {
	t := strings.TrimSpace(typeRef)
	if i := strings.LastIndexByte(t, ':'); i >= 0 {
		t = t[i+1:]
	}
	switch strings.ToLower(t) {
	case "":
		return ""
	case "number", "integer", "long", "double", "decimal":
		return model.TypeNumber
	case "string", "text":
		return model.TypeString
	case "boolean", "bool":
		return model.TypeBoolean
	case "date":
		return model.TypeDate
	case "time":
		return model.TypeTime
	case "date and time", "datetime", "dateandtime":
		return model.TypeDateTime
	case "days and time duration", "daytimeduration":
		return model.TypeDayTimeDur
	case "years and months duration", "yearmonthduration":
		return model.TypeYearMonth
	case "any":
		return ""
	default:
		return t
	}
}

func coerceNumber(v any) (any, error) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return nil, fmt.Errorf("%q is not a number", x)
		}
		return feel.FromGo(json.Number(s)), nil
	case bool:
		return nil, fmt.Errorf("expected a number, got a boolean")
	case nil:
		return nil, fmt.Errorf("expected a number, got null")
	}
	out := feel.FromGo(v)
	if feel.TypeName(out) != "number" {
		return nil, fmt.Errorf("expected a number, got %T", v)
	}
	return out, nil
}

func coerceBool(v any) (any, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes":
			return true, nil
		case "false", "no":
			return false, nil
		}
		return nil, fmt.Errorf("%q is not a boolean", x)
	}
	return nil, fmt.Errorf("expected a boolean, got %T", v)
}

func coerceTemporal(v any, want string) (any, error) {
	out := feel.FromGo(v)
	if s, ok := v.(string); ok {
		parsed, err := feel.ParseTemporal(strings.TrimSpace(s), want)
		if err != nil {
			return nil, err
		}
		return parsed, nil
	}
	if feel.TypeName(out) == want || (want == model.TypeDateTime && feel.TypeName(out) == "date and time") {
		return out, nil
	}
	return nil, fmt.Errorf("expected %s, got %T", want, v)
}

func toSlice(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case nil:
		return nil, false
	}
	if converted, ok := feel.ToGo(feel.FromGo(v)).([]any); ok {
		return converted, true
	}
	return nil, false
}

func toMap(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case string:
		// A model asked for structured output sometimes answers with a JSON
		// string. Parsing it is a representation fix, not a semantic guess.
		var out map[string]any
		if err := json.Unmarshal([]byte(x), &out); err == nil {
			return out, true
		}
		return nil, false
	case nil:
		return nil, false
	}
	if converted, ok := feel.ToGo(feel.FromGo(v)).(map[string]any); ok {
		return converted, true
	}
	return nil, false
}

func fieldNames(components map[string]model.TypeSpec) string {
	names := make([]string, 0, len(components))
	for k := range components {
		names = append(names, k)
	}
	return strings.Join(names, ", ")
}
