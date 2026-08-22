// Package feel adapts the FEEL implementation Verdict embeds
// (github.com/pbinitiative/feel) to the shape the DMN evaluator needs:
// a compile cache, unary-test evaluation with `?` bound, dialect gating between
// S-FEEL and full FEEL, a namespaced extension point for application function
// libraries, and lossless conversion between FEEL values and plain Go values.
//
// Everything the rest of Verdict does with expressions goes through this
// package, so swapping the underlying evaluator is a single-package change.
package feel

import (
	"fmt"
	"sync"

	pfeel "github.com/frankbardon/verdict/pkg/feel/internal/dialect"
)

// Dialect selects the accepted expression grammar.
type Dialect int

const (
	// FEEL is the full DMN Conformance Level 3 dialect.
	FEEL Dialect = iota
	// SFEEL is the Conformance Level 2 subset: literals, arithmetic,
	// comparisons, ranges, lists, conjunctions and qualified names. Iteration,
	// conditionals and inline function definitions are rejected at compile time.
	SFEEL
)

func (d Dialect) String() string {
	if d == SFEEL {
		return "s-feel"
	}
	return "feel"
}

// Expr is a compiled FEEL expression. Expr values are immutable and safe for
// concurrent evaluation.
type Expr struct {
	text string
	node pfeel.Node
}

// Text returns the source the expression was compiled from.
func (e *Expr) Text() string {
	if e == nil {
		return ""
	}
	return e.text
}

// Node exposes the underlying AST for analysis passes.
func (e *Expr) Node() pfeel.Node {
	if e == nil {
		return nil
	}
	return e.node
}

// CompileError reports an expression that failed to parse or that used a
// construct the active dialect forbids.
type CompileError struct {
	Text    string
	Dialect Dialect
	Reason  string
	Err     error
}

func (e *CompileError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("feel: cannot compile %q: %v", e.Text, e.Err)
	}
	return fmt.Sprintf("feel: %q is not valid %s: %s", e.Text, e.Dialect, e.Reason)
}

func (e *CompileError) Unwrap() error { return e.Err }

// Compiler parses and caches FEEL expressions for one dialect. A Compiler is
// safe for concurrent use and is normally shared for the lifetime of an Engine.
type Compiler struct {
	dialect Dialect
	cache   sync.Map // string -> *cacheEntry
	utCache sync.Map // string -> *unaryEntry
}

type cacheEntry struct {
	once sync.Once
	expr *Expr
	err  error
}

// NewCompiler returns a compiler for the given dialect.
func NewCompiler(d Dialect) *Compiler { return &Compiler{dialect: d} }

// Dialect reports the compiler's dialect.
func (c *Compiler) Dialect() Dialect { return c.dialect }

// Compile parses text, memoising both successes and failures so a decision
// table with a hot cell pays the parse cost once per process.
func (c *Compiler) Compile(text string) (*Expr, error) {
	v, _ := c.cache.LoadOrStore(text, &cacheEntry{})
	e := v.(*cacheEntry)
	e.once.Do(func() {
		e.expr, e.err = c.compile(text)
	})
	return e.expr, e.err
}

func (c *Compiler) compile(text string) (*Expr, error) {
	node, err := pfeel.ParseString(text)
	if err != nil {
		return nil, &CompileError{Text: text, Dialect: c.dialect, Err: err}
	}
	if c.dialect == SFEEL {
		if reason := violatesSFEEL(node); reason != "" {
			return nil, &CompileError{Text: text, Dialect: c.dialect, Reason: reason}
		}
	}
	return &Expr{text: text, node: node}, nil
}

// IsIrrelevant reports whether a decision-table input entry means "any value".
// DMN spells this "-"; several exporters emit an empty cell instead, and dmn-js
// writes the Unicode dashes when a modeller types them.
func IsIrrelevant(text string) bool {
	switch trimSpace(text) {
	case "", "-", "–", "—":
		return true
	default:
		return false
	}
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}
