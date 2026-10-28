package dialect

// for FEEL syntax refer to https://learn-dmn-in-15-minutes.com/learn/the-feel-language.html
// for BNF forms and handbook refer to https://kiegroup.github.io/dmn-feel-handbook

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
)

type UnexpectedToken struct {
	token   ScannerToken
	callers []string
	expects []string
}

func NewUnexpectedToken(token ScannerToken, callers []string, expects []string) *UnexpectedToken {
	return &UnexpectedToken{token: token, callers: callers, expects: expects}
}

// VERDICT DIVERGENCE: Error() no longer appends the parser's Go call stack.
// The message reaches a modeller through a VERDICT_LOAD_013 diagnostic about a
// cell in their decision table, and ten frames of dialect.(*Parser) internals
// there are noise that obscures the one line they can act on. The stack is
// still captured; Callers exposes it for debugging this package.
func (self UnexpectedToken) Error() string {
	return fmt.Sprintf(
		"unexpected %s %s, at %s, expect %s",
		self.token.Kind, self.token.Value,
		self.token.Pos,
		strings.Join(self.expects, ", "),
	)
}

// Callers returns the parser frames captured when the error was raised.
func (self UnexpectedToken) Callers() []string { return self.callers }

func hasDupName(names []string) (bool, string) {
	nameSet := make(map[string]bool)
	for _, name := range names {
		if _, ok := nameSet[name]; ok {
			return true, name
		}
		nameSet[name] = true
	}
	return false, ""
}

func ParseString(input string) (Node, error) {
	parser := NewParser(NewScanner(input))
	return parser.Parse()
}

type Parser struct {
	scanner *Scanner
	// noIndex suppresses `[` as a list-index suffix while an interval's upper
	// endpoint is being parsed. VERDICT DIVERGENCE: without it, `[1..10[` reads
	// the closing bracket as the start of an index on `10` and the interval
	// never terminates. An endpoint that genuinely needs an index can be
	// parenthesised.
	noIndex int
}

func NewParser(scanner *Scanner) *Parser {
	return &Parser{
		scanner: scanner,
	}
}

func (p Parser) Unexpected(expects ...string) *UnexpectedToken {
	// extract caller stack dump
	pc := make([]uintptr, 10)
	n := runtime.Callers(2, pc)
	var callers []string
	if n > 0 {
		pc = pc[:n]
		frames := runtime.CallersFrames(pc)
		for {
			frame, more := frames.Next()
			callers = append(callers, fmt.Sprintf("%s:%d", frame.Function, frame.Line))
			if !more {
				break
			}
		}
	}
	return NewUnexpectedToken(p.CurrentToken(), callers, expects)
}

func (p Parser) CurrentToken() ScannerToken {
	return p.scanner.Current()
}

func (p *Parser) Parse() (Node, error) {
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	var exps []Node

	for !p.CurrentToken().Expect(TokenEOF) {
		if p.CurrentToken().Expect(";") {
			err = p.scanner.Next()
			if err != nil {
				return nil, err
			}
		} else {
			exp, err := p.parseUnaryTest()
			if err != nil {
				return nil, err
			}
			exps = append(exps, exp)
		}
	}

	if len(exps) == 1 {
		return exps[0], nil
	} else {
		return &ExprList{
			Elements: exps,
		}, nil
	}
}

func (p Parser) startTextRange() TextRange {
	return TextRange{Start: p.CurrentToken().Pos}
}

func (p *Parser) parseUnaryTestElement() (Node, error) {
	if p.CurrentToken().Expect(">", ">=", "<", "<=", "!=", "=") {
		textRange := p.startTextRange()
		op := p.CurrentToken().Kind
		err := p.scanner.Next()
		if err != nil {
			return nil, err
		}
		right, err := p.expression()
		if err != nil {
			return nil, err
		}
		textRange.End = p.CurrentToken().Pos
		exp := &Binop{
			Left:      &Var{Name: "?"},
			Op:        op,
			Right:     right,
			textRange: textRange,
		}
		return exp, nil
	} else {
		return p.expression()
	}
}

func (p *Parser) parseUnaryTest() (Node, error) {
	textRange := p.startTextRange()
	exp, err := p.parseUnaryTestElement()
	if err != nil {
		return nil, err
	}

	if p.CurrentToken().Expect(",") {
		elements := []Node{exp}
		for p.CurrentToken().Expect(",") {
			err = p.scanner.Next()
			if err != nil {
				return nil, err
			}

			uexp, err := p.parseUnaryTestElement()
			if err != nil {
				return nil, err
			}
			elements = append(elements, uexp)
		}
		textRange.End = p.CurrentToken().Pos
		return &MultiTests{Elements: elements, textRange: textRange}, nil
	} else {
		return exp, nil
	}
}

func (p *Parser) expression() (Node, error) {
	return p.logicOrOp()
}

type astFunc func() (Node, error)

func (p *Parser) binop(ops []string, subfunc astFunc) (Node, error) {
	left, err := subfunc()
	if err != nil {
		return nil, err
	}

	for p.CurrentToken().Expect(ops...) {
		op := p.CurrentToken().Kind
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}

		right, err := subfunc()
		if err != nil {
			return nil, err
		}
		textRange := TextRange{Start: left.TextRange().Start}
		textRange.End = p.CurrentToken().Pos
		left = &Binop{Op: op, Left: left, Right: right, textRange: textRange}
	}
	return left, nil
}

func (p *Parser) binopKeywords(ops []string, subfunc astFunc) (Node, error) {
	left, err := subfunc()
	if err != nil {
		return nil, err
	}

	for p.CurrentToken().ExpectKeywords(ops...) {
		op := p.CurrentToken().Value
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}

		right, err := subfunc()
		if err != nil {
			return nil, err
		}
		textRange := TextRange{Start: left.TextRange().Start}
		textRange.End = p.CurrentToken().Pos

		left = &Binop{Op: op, Left: left, Right: right, textRange: textRange}
	}
	return left, nil
}

// pase chains
func (p *Parser) logicOrOp() (Node, error) {
	return p.binopKeywords(
		[]string{"or"},
		p.logicAndOp,
	)
}

func (p *Parser) logicAndOp() (Node, error) {
	return p.binopKeywords(
		[]string{"and"},
		p.compareOp,
	)
}

// compareOp parses a comparison, DMN 1.5 FEEL grammar rule 49: the ordering
// and equality operators (49.a) and `in` (49.c, 49.d), all at one precedence
// level between conjunction and arithmetic, left associative, and `between`
// (49.b).
//
// VERDICT DIVERGENCE: upstream parsed `in` as its own level above `or`, so
// `x in [5] and b` was `x in ([5] and b)`, and read the right of `in` as a
// plain expression, so `10 in =10` and `10 in (1, < 5, >= 10)` were syntax
// errors. The right of `in` is now a positive unary test or a parenthesised
// list of them (see inRight). Upstream did not parse `between` (49.b) at all;
// see betweenRest. Rule 51 (`instance of`) binds tighter than arithmetic, so
// it is not a comparison (see instanceOfOp).
func (p *Parser) compareOp() (Node, error) {
	left, err := p.addOrSubOp()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.CurrentToken()
		switch {
		case tok.Expect(">", ">=", "<", "<=", "!=", "="):
			if err := p.scanner.Next(); err != nil {
				return nil, err
			}
			right, err := p.addOrSubOp()
			if err != nil {
				return nil, err
			}
			left = &Binop{Op: tok.Kind, Left: left, Right: right, textRange: TextRange{Start: left.TextRange().Start, End: p.CurrentToken().Pos}}
		case tok.ExpectKeywords("in"):
			if err := p.scanner.Next(); err != nil {
				return nil, err
			}
			right, err := p.inRight()
			if err != nil {
				return nil, err
			}
			left = &Binop{Op: "in", Left: left, Right: right, textRange: TextRange{Start: left.TextRange().Start, End: p.CurrentToken().Pos}}
		case tok.Kind == TokenName && tok.Value == "between":
			left, err = p.betweenRest(left)
			if err != nil {
				return nil, err
			}
		default:
			return left, nil
		}
	}
}

// inRight parses the right of `in`: a positive unary test (rule 49.c) or a
// parenthesised, comma-separated list of them (rule 49.d). A test led by a
// comparison operator (`= 10`, `< 5`) and a parenthesised list come back as
// MultiTests, whose `? op x` elements Binop.inOp applies to the left operand;
// anything else is an ordinary operand, so `x in [1..5]`, `x in (1..5)`,
// `x in xs` and `x in (a)` parse exactly as before.
//
// `(` is ambiguous between an interval or parenthesised expression and a list
// of tests, so it is first read as an ordinary operand and, only if that fails
// (`(1, < 5)`, `(< 5)`), re-read from the same token as a list of tests.
func (p *Parser) inRight() (Node, error) {
	textRange := p.startTextRange()
	if p.CurrentToken().Expect(">", ">=", "<", "<=", "!=", "=") {
		op := p.CurrentToken().Kind
		if err := p.scanner.Next(); err != nil {
			return nil, err
		}
		endpoint, err := p.addOrSubOp()
		if err != nil {
			return nil, err
		}
		textRange.End = p.CurrentToken().Pos
		test := &Binop{Left: &Var{Name: "?"}, Op: op, Right: endpoint, textRange: textRange}
		return &MultiTests{Elements: []Node{test}, textRange: textRange}, nil
	}
	if !p.CurrentToken().Expect("(") {
		return p.addOrSubOp()
	}
	saved, savedNoIndex := *p.scanner, p.noIndex
	operand, err := p.addOrSubOp()
	if err == nil {
		return operand, nil
	}
	*p.scanner, p.noIndex = saved, savedNoIndex
	if err := p.scanner.Next(); err != nil {
		return nil, err
	}
	tests, err := p.delimited(p.parseUnaryTest)
	if err != nil {
		return nil, err
	}
	if !p.CurrentToken().Expect(")") {
		return nil, p.Unexpected(",", ")")
	}
	if err := p.scanner.Next(); err != nil {
		return nil, err
	}
	textRange.End = p.CurrentToken().Pos
	if mt, ok := tests.(*MultiTests); ok {
		return &MultiTests{Elements: mt.Elements, textRange: textRange}, nil
	}
	return &MultiTests{Elements: []Node{tests}, textRange: textRange}, nil
}

// betweenRest parses `between e2 and e3` after the left operand, DMN 1.5
// grammar rule 49.b. Both bounds are arithmetic expressions (addOrSubOp), so
// the `and` separating them is consumed here and never read as conjunction:
// `x between 1 and 5 and b` is `(x between 1 and 5) and b`, and a bound that
// needs a lower-precedence operator must be parenthesised.
//
// A name cannot start with `between` (§10.3.1.4: a name start is never a
// literal terminal), so after a complete operand the word can only be the
// operator, and a malformed `3 between 1` is a syntax error.
//
// VERDICT DIVERGENCE: upstream had no `between`. It is not a scanner keyword:
// `between` stays an ordinary name word so a name that merely contains it
// (`days between dates`) still resolves; see atBetween.
func (p *Parser) betweenRest(left Node) (Node, error) {
	if err := p.scanner.Next(); err != nil {
		return nil, err
	}
	low, err := p.addOrSubOp()
	if err != nil {
		return nil, err
	}
	if !p.CurrentToken().ExpectKeywords("and") {
		return nil, p.Unexpected("and")
	}
	if err := p.scanner.Next(); err != nil {
		return nil, err
	}
	high, err := p.addOrSubOp()
	if err != nil {
		return nil, err
	}
	return &Between{Expr: left, Low: low, High: high, textRange: TextRange{Start: left.TextRange().Start, End: p.CurrentToken().Pos}}, nil
}

// atBetween reports, for parseName, whether the name word `between` starts a
// rule 49.b `between`: it is followed by an arithmetic expression and the
// keyword `and`. It parses ahead speculatively and restores the scanner, as
// inRight backtracks.
//
// Interpretation: DMN 1.5 §10.3.1.4 lets a name part (not a name start) be a
// literal terminal such as `between`, and resolves the ambiguity against the
// names in scope. The fork resolves names without scope, so it reads `between`
// as the operator exactly when `between e and` follows; otherwise the word
// stays part of the name (`days between dates`), and so a malformed `between`
// after a name (`x between 1`) reads as the name `x between` followed by a
// juxtaposed `1`, as any other juxtaposition does. A name containing
// `between e and` (`gap between rows and columns`) cannot be written.
func (p *Parser) atBetween() bool {
	tok := p.CurrentToken()
	if tok.Kind != TokenName || tok.Value != "between" {
		return false
	}
	saved, savedNoIndex := *p.scanner, p.noIndex
	defer func() { *p.scanner, p.noIndex = saved, savedNoIndex }()
	if err := p.scanner.Next(); err != nil {
		return false
	}
	if _, err := p.addOrSubOp(); err != nil {
		return false
	}
	return p.CurrentToken().ExpectKeywords("and")
}

func (p *Parser) addOrSubOp() (Node, error) {
	return p.binop(
		[]string{"+", "-"},
		p.mulOrDivOp,
	)
}

func (p *Parser) mulOrDivOp() (Node, error) {
	return p.binop(
		[]string{"*", "/", "%"},
		p.powOp,
	)
}

// VERDICT DIVERGENCE: exponentiation and arithmetic negation (DMN 1.5 grammar
// rules 4.c and 4.d). Upstream parsed neither: a leading `-` was only ever part
// of a numeric literal. `**` binds tighter than `*` and `/` and associates
// left, as the fork's other binary operators do; negation binds tighter than
// `**`, matching upstream's negative numeric literal, so `-x ** 2` is
// `(-x) ** 2` just as `-2 ** 2` is 4. The scanner no longer folds a minus into
// a literal (see TokenNumber), so every leading `-` reaches negationOp.
func (p *Parser) powOp() (Node, error) {
	return p.binop(
		[]string{"**"},
		p.instanceOfOp,
	)
}

// instanceOfOp parses `e instance of type`, DMN 1.5 grammar rule 51.
//
// VERDICT DIVERGENCE: upstream had no `instance of`; `x instance of number`
// scanned as one variable name and was null. Rule 2 orders the alternatives of
// a textual expression from lowest to highest precedence (§10.3.1.2, additional
// syntax rules) and puts `instance of` (2.f) after arithmetic (2.e) and before
// path, filter and invocation (2.g), so it binds tighter than every arithmetic
// operator and looser than a path: `1 + 1 instance of number` is
// `1 + (1 instance of number)`, and `a.b instance of number` checks `a.b`.
// Negation is the exception: it binds tighter, as the fork's negation already
// does against `**`, because rule 35 lets a numeric literal carry its own
// minus, so `-5 instance of number` is true.
//
// `instance` and `of` are not keywords, so names containing either word
// (`Date of Birth`, `instance count`) still parse; parseName ends a name only
// where `instance` is directly followed by `of`.
func (p *Parser) instanceOfOp() (Node, error) {
	left, err := p.negationOp()
	if err != nil {
		return nil, err
	}
	for p.atInstanceOf() {
		for i := 0; i < 2; i++ {
			if err := p.scanner.Next(); err != nil {
				return nil, err
			}
		}
		t, err := p.parseType()
		if err != nil {
			return nil, err
		}
		left = &InstanceOf{Expr: left, Type: t, textRange: TextRange{Start: left.TextRange().Start, End: p.CurrentToken().Pos}}
	}
	return left, nil
}

// atInstanceOf reports whether the next two tokens are the names `instance`
// and `of`. It looks ahead on a copy of the scanner, as inRight backtracks.
func (p *Parser) atInstanceOf() bool {
	tok := p.CurrentToken()
	if tok.Kind != TokenName || tok.Value != "instance" {
		return false
	}
	ahead := *p.scanner
	if err := ahead.Next(); err != nil {
		return false
	}
	next := ahead.Current()
	return next.Kind == TokenName && next.Value == "of"
}

// parseType parses a type, DMN 1.5 grammar rule 52. A generic name written
// without its parameters (`list`, `context`, `function`) is kept as a plain
// name, which maps to no lattice node, so the check is null (Table 61:
// "123 instance of list is null as a list type requires parameters").
//
// Interpretation: rule 52 as printed requires at least one context entry, but
// TCK 0070 writes `context<>`; it parses as the context type with no entries,
// to which every context conforms.
func (p *Parser) parseType() (*TypeExpr, error) {
	tok := p.CurrentToken()
	if tok.ExpectKeywords("function") {
		if err := p.scanner.Next(); err != nil {
			return nil, err
		}
		if !p.CurrentToken().Expect("<") {
			return &TypeExpr{Kind: TypeKindName, Name: "function"}, nil
		}
		if err := p.scanner.Next(); err != nil {
			return nil, err
		}
		var params []*TypeExpr
		for !p.CurrentToken().Expect(">") {
			param, err := p.parseType()
			if err != nil {
				return nil, err
			}
			params = append(params, param)
			if p.CurrentToken().Expect(",") {
				if err := p.scanner.Next(); err != nil {
					return nil, err
				}
			} else if !p.CurrentToken().Expect(">") {
				return nil, p.Unexpected(",", ">")
			}
		}
		for _, want := range []string{">", "-", ">"} {
			if !p.CurrentToken().Expect(want) {
				return nil, p.Unexpected("->")
			}
			if err := p.scanner.Next(); err != nil {
				return nil, err
			}
		}
		result, err := p.parseType()
		if err != nil {
			return nil, err
		}
		return &TypeExpr{Kind: TypeKindFunction, Params: params, Result: result}, nil
	}
	if tok.Kind != TokenName {
		return nil, p.Unexpected("type")
	}
	name, err := p.parseTypeName()
	if err != nil {
		return nil, err
	}
	if p.CurrentToken().Expect("<") {
		switch name {
		case TypeKindList, TypeKindRange:
			if err := p.scanner.Next(); err != nil {
				return nil, err
			}
			elem, err := p.parseType()
			if err != nil {
				return nil, err
			}
			if err := p.expectTypeClose(); err != nil {
				return nil, err
			}
			return &TypeExpr{Kind: name, Elem: elem}, nil
		case TypeKindContext:
			return p.parseContextType()
		}
	}
	for p.CurrentToken().Expect(".") {
		if err := p.scanner.Next(); err != nil {
			return nil, err
		}
		part, err := p.parseTypeName()
		if err != nil {
			return nil, err
		}
		name += "." + part
	}
	return &TypeExpr{Kind: TypeKindName, Name: name}, nil
}

func (p *Parser) parseContextType() (*TypeExpr, error) {
	if err := p.scanner.Next(); err != nil {
		return nil, err
	}
	t := &TypeExpr{Kind: TypeKindContext}
	for !p.CurrentToken().Expect(">") {
		key, err := p.parseMapKey()
		if err != nil {
			return nil, err
		}
		if !p.CurrentToken().Expect(":") {
			return nil, p.Unexpected(":")
		}
		if err := p.scanner.Next(); err != nil {
			return nil, err
		}
		field, err := p.parseType()
		if err != nil {
			return nil, err
		}
		t.Keys = append(t.Keys, key)
		t.Fields = append(t.Fields, field)
		if p.CurrentToken().Expect(",") {
			if err := p.scanner.Next(); err != nil {
				return nil, err
			}
		} else if !p.CurrentToken().Expect(">") {
			return nil, p.Unexpected(",", ">")
		}
	}
	if err := p.expectTypeClose(); err != nil {
		return nil, err
	}
	return t, nil
}

func (p *Parser) expectTypeClose() error {
	if !p.CurrentToken().Expect(">") {
		return p.Unexpected(">")
	}
	return p.scanner.Next()
}

// parseTypeName parses one segment of a type's qualified name. It is
// parseName, except that a keyword taken in as part of `date and time`, `days
// and time duration` or `years and months duration` is given back unless the
// whole of one of those names follows: `x instance of date and y` is
// `(x instance of date) and y`, not a check against the type `date and y`.
func (p *Parser) parseTypeName() (string, error) {
	var names []string
	var saved *Scanner
	savedLen := 0
	for p.CurrentToken().Expect(TokenName, TokenKeyword) {
		tok := p.CurrentToken()
		if len(names) > 0 && p.atInstanceOf() {
			break
		}
		if tok.Kind == TokenKeyword {
			if len(names) == 0 || !keywordNamePrefixes[strings.Join(append(names, tok.Value), " ")] {
				break
			}
			if saved == nil {
				s := *p.scanner
				saved, savedLen = &s, len(names)
			}
		}
		names = append(names, tok.Value)
		if err := p.scanner.Next(); err != nil {
			return "", err
		}
	}
	if len(names) == 0 {
		return "", p.Unexpected(TokenName)
	}
	name := strings.Join(names, " ")
	if _, primitive := primitiveTypes[name]; saved != nil && !primitive {
		*p.scanner = *saved
		name = strings.Join(names[:savedLen], " ")
	}
	return name, nil
}

func (p *Parser) negationOp() (Node, error) {
	if !p.CurrentToken().Expect("-") {
		return p.parseFuncallOrIndexOrDot()
	}
	textRange := p.startTextRange()
	if err := p.scanner.Next(); err != nil {
		return nil, err
	}
	operand, err := p.negationOp()
	if err != nil {
		return nil, err
	}
	textRange.End = p.CurrentToken().Pos
	return &Negation{Operand: operand, textRange: textRange}, nil
}

func (p *Parser) parseFuncallOrIndexOrDot() (Node, error) {
	exp, err := p.singleElement()
	if err != nil {
		return nil, err
	}
	for {
		switch p.CurrentToken().Kind {
		case "(":
			nexp, err := p.parseFuncallRest(exp)
			if err != nil {
				return nil, err
			}
			exp = nexp
		case "[":
			if p.noIndex > 0 {
				return exp, nil
			}
			nexp, err := p.parseIndexRest(exp)
			if err != nil {
				return nil, err
			}
			exp = nexp
		case ".":
			nexp, err := p.parseDotRest(exp)
			if err != nil {
				return nil, err
			}
			exp = nexp
		default:
			return exp, nil
		}
	}
}

func (p *Parser) parseFunccallArg() (funcallArg, error) {
	arg, err := p.expression()
	if err != nil {
		return funcallArg{}, err
	}

	if p.CurrentToken().Expect(":") { // kwargs
		if varArg, ok := arg.(*Var); ok {
			err = p.scanner.Next()
			if err != nil {
				return funcallArg{}, err
			}
			argValue, err := p.expression()
			if err != nil {
				return funcallArg{}, err
			}
			return funcallArg{argName: varArg.Name, arg: argValue}, nil
		} else {
			return funcallArg{}, p.Unexpected("var")
		}
	} else {
		return funcallArg{argName: "", arg: arg}, nil
	}
}

func (p *Parser) parseFuncallRest(funExpr Node) (Node, error) {
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	// parse function arguments
	var args []funcallArg = nil
	keywordArgs := false
	for !p.CurrentToken().Expect(")") {
		arg, err := p.parseFunccallArg()
		if err != nil {
			return nil, err
		}
		if !keywordArgs && arg.argName != "" {
			keywordArgs = true
		}
		if len(args) > 0 {
			if arg.argName != "" && args[0].argName == "" {
				return nil, p.Unexpected("non var")
			}
			if arg.argName == "" && args[0].argName != "" {
				return nil, p.Unexpected("var")
			}
		}
		args = append(args, arg)
		if p.CurrentToken().Expect(",") {
			err = p.scanner.Next()
			if err != nil {
				return nil, err
			}
		} else if !p.CurrentToken().Expect(")") {
			return nil, p.Unexpected(",", ")")
		}
	}

	if p.CurrentToken().Expect(")") {
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
	}

	textRange := TextRange{Start: funExpr.TextRange().Start, End: p.CurrentToken().Pos}
	return &FunCall{
		FunRef:      funExpr,
		Args:        args,
		keywordArgs: keywordArgs,
		textRange:   textRange,
	}, nil
}

func (p *Parser) parseIndexRest(exp Node) (Node, error) {
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}

	// parse index arguments
	at, err := p.delimited(p.expression)
	if err != nil {
		return nil, err
	}
	if !p.CurrentToken().Expect("]") {
		return nil, p.Unexpected("]")
	}

	err = p.scanner.Next()
	if err != nil {
		return nil, err
	}
	textRange := TextRange{Start: exp.TextRange().Start, End: p.CurrentToken().Pos}

	return &Binop{Left: exp, Op: "[]", Right: at, textRange: textRange}, nil
}

func (p *Parser) parseDotRest(exp Node) (Node, error) {
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	// parse index arguments
	attr, err := p.parseName()
	if err != nil {
		return nil, err
	}
	textRange := TextRange{Start: exp.TextRange().Start, End: p.CurrentToken().Pos}
	return &DotOp{Left: exp, Attr: attr, textRange: textRange}, nil
}

func (p *Parser) singleElement() (Node, error) {
	curr := p.CurrentToken()
	switch curr.Kind {
	case TokenName:
		return p.parseVar()
	// case TokenFuncall:
	// 	return p.parseFuncall()
	case TokenNumber:
		return p.parseNumberNode()
	case TokenString:
		return p.parseStringNode()
	case TokenTemporal:
		return p.parseTemporalNode()
	case "(":
		return p.parseBracketOrRange()
	case "[":
		return p.parseRangeOrArray()
	// VERDICT DIVERGENCE: `]` opens an exclusive interval — DMN 1.5 §10.3.1.2 (grammar rules 9, 11)
	// admits `]a..b]` alongside `(a..b]`. A closing bracket can never begin an
	// expression, so routing it here is unambiguous.
	case "]":
		return p.parseRangeOrArray()
	case "{":
		return p.parseMapNode()
	case "?":
		return &Var{Name: "?"}, nil
	case TokenKeyword:
		switch curr.Value {
		case "true":
			return p.parseBool()
		case "false":
			return p.parseBool()
		case "null":
			return p.parseNull()
		case "if":
			return p.parseIfExpression()
		case "for":
			return p.parseForExpr()
		case "function":
			return p.parseFunDef()
		case "some":
			return p.parseSomeOrEvery()
		case "every":
			return p.parseSomeOrEvery()
		default:
			//return nil, p.Unexpected("keywords")
			// unexpected keywords can be part of names
			return p.parseVar()
		}
	default:
		return nil, p.Unexpected("name", "number", "string", "(", "[", "keyword")
	}
}

func (p *Parser) parseVar() (Node, error) {
	textRange := p.startTextRange()
	name, err := p.parseName()
	if err != nil {
		return nil, err
	}
	textRange.End = p.CurrentToken().Pos
	return &Var{Name: name, textRange: textRange}, nil
}

func (p *Parser) parseBool() (Node, error) {
	textRange := p.startTextRange()
	v := p.CurrentToken().Value
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	textRange.End = p.CurrentToken().Pos
	switch v {
	case "true":
		return &BoolNode{Value: true, textRange: textRange}, nil
	case "false":
		return &BoolNode{Value: false, textRange: textRange}, nil
	default:
		return nil, p.Unexpected("true", "false")
	}
}

func (p *Parser) parseNull() (Node, error) {
	textRange := p.startTextRange()
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	textRange.End = p.CurrentToken().Pos
	return &NullNode{textRange: textRange}, nil
}

func containsKeywords(keywords []string, kw string) bool {
	for _, stopKw := range keywords {
		if stopKw == kw {
			return true
		}
	}
	return false
}

// keywordNamePrefixes lists every prefix of a DMN built-in name that contains a
// reserved keyword. It exists to resolve the one genuine ambiguity in FEEL's
// name grammar.
//
// FEEL allows names to contain spaces (`Credit Rating`), and the DMN standard
// library uses that for `date and time`, `days and time duration` and `years
// and months duration` — all of which embed the reserved word `and`. Upstream
// resolved the ambiguity by letting a name absorb *any* keyword, which silently
// reinterprets `x and y` as a single variable named "x and y" and makes
// `if x then a else b` a parse error. Verdict resolves it the other way: a
// keyword ends a name unless the result is still a prefix of one of the three
// standard names below.
//
// The cost is that a model variable whose name contains a reserved word cannot
// be referenced unqualified. That matches every other FEEL implementation, and
// it is a far better trade than `and` silently ceasing to be a conjunction.
var keywordNamePrefixes = map[string]bool{
	"date and":                  true,
	"date and time":             true,
	"days and":                  true,
	"days and time":             true,
	"days and time duration":    true,
	"years and":                 true,
	"years and months":          true,
	"years and months duration": true,
}

func (p *Parser) parseName(stopKeywords ...string) (string, error) {
	names := make([]string, 0)

	for p.CurrentToken().Expect(TokenName, TokenKeyword) {
		if p.CurrentToken().Kind == TokenName {
			// `instance of` ends a name (see instanceOfOp), as does a
			// `between e and` (see atBetween).
			if len(names) > 0 && (p.atInstanceOf() || p.atBetween()) {
				break
			}
			names = append(names, p.CurrentToken().Value)
			if err := p.scanner.Next(); err != nil {
				return "", err
			}
			continue
		}

		kwVal := p.CurrentToken().Value
		if len(names) > 0 {
			if containsKeywords(stopKeywords, kwVal) {
				break
			}
			if !keywordNamePrefixes[strings.Join(append(names, kwVal), " ")] {
				break
			}
		}
		names = append(names, kwVal)
		if err := p.scanner.Next(); err != nil {
			return "", err
		}
	}
	if len(names) <= 0 {
		return "", p.Unexpected(TokenName)
	}
	return strings.Join(names, " "), nil
}

// endpointExpression parses an interval's upper endpoint with `[` reserved for
// closing the interval rather than indexing the endpoint.
func (p *Parser) endpointExpression() (Node, error) {
	p.noIndex++
	defer func() { p.noIndex-- }()
	return p.expression()
}

// delimited parses a subexpression that its own closing delimiter terminates —
// the body of `(...)`, an index, an array element, a map value. Inside one, a
// `[` cannot be an interval's closing bracket, so indexing means indexing
// again. This is what makes `[1..(xs[1])]` a workable spelling of the endpoint
// index that endpointExpression otherwise reserves.
func (p *Parser) delimited(parse func() (Node, error)) (Node, error) {
	saved := p.noIndex
	p.noIndex = 0
	defer func() { p.noIndex = saved }()
	return parse()
}

func (p *Parser) parseBracketOrRange() (Node, error) {
	textRange := p.startTextRange()
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	c, err := p.delimited(p.expression)
	if err != nil {
		return nil, err
	}
	if p.CurrentToken().Kind == ".." {
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
		d, err := p.endpointExpression()
		if err != nil {
			return nil, err
		}

		if p.CurrentToken().Expect(")", "[") {
			err = p.scanner.Next()
			if err != nil {
				return nil, err
			}
			textRange.End = p.CurrentToken().Pos
			return &RangeNode{StartOpen: true, Start: c, EndOpen: true, End: d, textRange: textRange}, nil
		} else if p.CurrentToken().Kind == "]" {
			err = p.scanner.Next()
			if err != nil {
				return nil, err
			}
			textRange.End = p.CurrentToken().Pos
			return &RangeNode{StartOpen: true, Start: c, EndOpen: false, End: d, textRange: textRange}, nil
		}
		return nil, p.Unexpected(")", "]")
	} else if p.CurrentToken().Expect(")") {
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
	} else {
		return nil, p.Unexpected(")")
	}
	return c, nil
}

func (p *Parser) parseRangeOrArray() (Node, error) {
	rng := p.startTextRange()
	// prefixKind is '[' or, for an exclusive lower bound, ']'.
	prefixKind := p.CurrentToken().Kind
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	if prefixKind != "]" && p.CurrentToken().Expect("]") {
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
		// empty array
		return &ArrayNode{}, nil
	}
	c, err := p.delimited(p.expression)
	if err != nil {
		return nil, err
	}

	// A ']' prefix can only open an interval; `]1, 2]` is not a list.
	if prefixKind != "]" && p.CurrentToken().Expect(",", "]") {
		return p.parseArrayGivenFirst(prefixKind, c)
	}

	if !p.CurrentToken().Expect("..") {
		return nil, p.Unexpected("..")
	}
	err = p.scanner.Next()
	if err != nil {
		return nil, err
	}
	d, err := p.endpointExpression()
	if err != nil {
		return nil, err
	}

	startOpen := prefixKind == "(" || prefixKind == "]"
	// VERDICT DIVERGENCE: `[` closes an exclusive interval — `[a..b[` is the
	// spec's equivalent of `[a..b)`, and dmn-js writes it.
	if p.CurrentToken().Expect(")", "[") {
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
		rng.End = p.CurrentToken().Pos
		return &RangeNode{StartOpen: startOpen, Start: c, EndOpen: true, End: d, textRange: rng}, nil
	} else if p.CurrentToken().Kind == "]" {
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
		rng.End = p.CurrentToken().Pos
		return &RangeNode{StartOpen: startOpen, Start: c, EndOpen: false, End: d, textRange: rng}, nil
	}
	return nil, p.Unexpected(")", "]", "[")
}

func (p *Parser) parseArrayGivenFirst(prefixKind string, firstElem Node) (Node, error) {
	rng := p.startTextRange()
	elements := []Node{firstElem}
	for p.CurrentToken().Expect(",") {
		err := p.scanner.Next()
		if err != nil {
			return nil, err
		}
		elem, err := p.delimited(p.expression)
		if err != nil {
			return nil, err
		}
		elements = append(elements, elem)
	}
	if !p.CurrentToken().Expect("]") {
		return nil, p.Unexpected("]")
	}
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	rng.End = p.CurrentToken().Pos
	return &ArrayNode{Elements: elements, textRange: rng}, nil
}

func (p *Parser) parseNumberNode() (Node, error) {
	rng := p.startTextRange()
	v := p.CurrentToken().Value
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	rng.End = p.CurrentToken().Pos
	return &NumberNode{Value: v, textRange: rng}, nil
}

func (p *Parser) parseStringNode() (Node, error) {
	rng := p.startTextRange()
	v := p.CurrentToken().Value
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	rng.End = p.CurrentToken().Pos
	return &StringNode{Value: v, textRange: rng}, nil
}

func (p *Parser) parseMapKey() (string, error) {
	switch p.CurrentToken().Kind {
	case TokenName:
		return p.parseName()
	case TokenString:
		node, err := p.parseStringNode()
		if err != nil {
			return "", err
		}
		return node.(*StringNode).Content(), nil
	default:
		return "", p.Unexpected(TokenName, TokenString)
	}
}

func (p *Parser) parseTemporalNode() (Node, error) {
	rng := p.startTextRange()
	tok := p.CurrentToken()
	v := tok.Value
	// VERDICT DIVERGENCE: an at-literal whose string is not a date, time, date
	// and time or duration string is a syntax error, raised here rather than
	// when the literal is evaluated. DMN 1.5 §10.3.1.2, additional syntax
	// rules: "The string in rule 65 must follow the date string, time string,
	// date and time string or duration string syntax". Unlike the conversion
	// built-ins (null outside their domain, §10.3.4), a malformed literal is
	// rejected at load, as VERDICT_LOAD_013. The position is the literal's own
	// token, 1-based (see ScanPosition.String).
	node := &TemporalNode{Value: v}
	if _, perr := ParseTemporalValue(node.Content()); perr != nil {
		return nil, fmt.Errorf("invalid temporal literal %s, at %s: %w",
			v, tok.Pos, perr)
	}
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	rng.End = p.CurrentToken().Pos
	node.textRange = rng
	return node, nil
}

func (p *Parser) parseMapNode() (Node, error) {
	rng := p.startTextRange()
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	var mapValues []mapItem

	for !p.CurrentToken().Expect("}") {
		key, err := p.parseMapKey()
		if err != nil {
			return nil, err
		}

		if !p.CurrentToken().Expect(":") {
			return nil, p.Unexpected(":")
		}
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
		exp, err := p.expression()
		if err != nil {
			return nil, err
		}

		mapValues = append(mapValues, mapItem{Name: key, Value: exp})

		if p.CurrentToken().Expect(",") {
			err = p.scanner.Next()
			if err != nil {
				return nil, err
			}
		} else if !p.CurrentToken().Expect("}") {
			return nil, p.Unexpected(",", "}")
		}
	}
	if p.CurrentToken().Expect("}") {
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
	}
	rng.End = p.CurrentToken().Pos
	return &MapNode{Values: mapValues, textRange: rng}, nil
}

func (p *Parser) parseIfExpression() (Node, error) {
	rng := p.startTextRange()
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	cond, err := p.expression()
	if err != nil {
		return nil, err
	}
	if !p.CurrentToken().ExpectKeywords("then") {
		return nil, p.Unexpected("then")
	}
	err = p.scanner.Next()
	if err != nil {
		return nil, err
	}

	then_branch, err := p.expression()
	if err != nil {
		return nil, err
	}
	if !p.CurrentToken().ExpectKeywords("else") {
		return nil, p.Unexpected("else")
	}
	err = p.scanner.Next()
	if err != nil {
		return nil, err
	}

	else_branch, err := p.expression()
	if err != nil {
		return nil, err
	}

	rng.End = p.CurrentToken().Pos
	return &IfExpr{Cond: cond, ThenBranch: then_branch, ElseBranch: else_branch, textRange: rng}, nil

}

func (p *Parser) parseForExpr() (Node, error) {
	rng := p.startTextRange()
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}
	varName, err := p.parseName("in", "for")

	if !p.CurrentToken().ExpectKeywords("in") {
		return nil, p.Unexpected("in")
	}
	err = p.scanner.Next()
	if err != nil {
		return nil, err
	}

	listExpr, err := p.iterationContext()
	if err != nil {
		return nil, err
	}

	if p.CurrentToken().Expect(",") {
		returnExpr, err := p.parseForExpr()
		if err != nil {
			return nil, err
		}
		// The remaining contexts iterate inside this one; Continues makes
		// Eval splice their results into one flat list rather than nest them.
		rng.End = p.CurrentToken().Pos
		return &ForExpr{
			Varname:    varName,
			ListExpr:   listExpr,
			ReturnExpr: returnExpr,
			Continues:  true,
			textRange:  rng,
		}, nil
	}

	if !p.CurrentToken().ExpectKeywords("return") {
		return nil, p.Unexpected("return")
	}
	err = p.scanner.Next()
	if err != nil {
		return nil, err
	}

	returnExpr, err := p.expression()
	if err != nil {
		return nil, err
	}
	rng.End = p.CurrentToken().Pos
	return &ForExpr{
		Varname:    varName,
		ListExpr:   listExpr,
		ReturnExpr: returnExpr,
		textRange:  rng,
	}, nil
}

// iterationContext parses the domain of one iteration context, DMN 1.5 FEEL
// grammar rule 63: `expression [ ".." expression ]`. The two-expression form
// is an integer range and parses to an IterationRange; upstream parsed only
// the list form, so `for i in 1..3` was a syntax error at the `..`.
//
// VERDICT DIVERGENCE: the spec's rule 46 gives `some` and `every` a plain
// expression, not an iteration context; the fork accepts the integer-range
// form there too (an open DMN issue proposes the same), see NOTICE.md.
func (p *Parser) iterationContext() (Node, error) {
	rng := p.startTextRange()
	start, err := p.expression()
	if err != nil {
		return nil, err
	}
	if p.CurrentToken().Kind != ".." {
		return start, nil
	}
	err = p.scanner.Next()
	if err != nil {
		return nil, err
	}
	end, err := p.expression()
	if err != nil {
		return nil, err
	}
	rng.End = p.CurrentToken().Pos
	return &IterationRange{Start: start, End: end, textRange: rng}, nil
}

// parseSomeOrEvery parses a quantified expression with one or more
// comma-separated iteration contexts, DMN 1.5 FEEL grammar rule 46:
// `some x in A, y in B satisfies P`. Upstream accepted a single context only.
// Several contexts desugar to nested quantifiers of the same kind, innermost
// last, the way parseForExpr nests `for`: a later context sees the names bound
// by earlier ones, and because the inner quantifier's three-valued result is
// folded by the outer one, the answer is the fold over the cartesian product.
func (p *Parser) parseSomeOrEvery() (Node, error) {
	rng := p.startTextRange()
	cmd := p.CurrentToken().Value
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}

	type iterContext struct {
		varName  string
		listExpr Node
	}
	var contexts []iterContext
	for {
		// parse variable name
		varName, err := p.parseName("in")
		if err != nil {
			return nil, err
		}

		if !p.CurrentToken().ExpectKeywords("in") {
			return nil, p.Unexpected("in")
		}
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}

		listExpr, err := p.iterationContext()
		if err != nil {
			return nil, err
		}
		contexts = append(contexts, iterContext{varName: varName, listExpr: listExpr})

		if !p.CurrentToken().Expect(",") {
			break
		}
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
	}

	if !p.CurrentToken().ExpectKeywords("satisfies") {
		return nil, p.Unexpected("satisfies")
	}
	err = p.scanner.Next()
	if err != nil {
		return nil, err
	}

	filterExpr, err := p.expression()
	if err != nil {
		return nil, err
	}
	rng.End = p.CurrentToken().Pos

	node := filterExpr
	for i := len(contexts) - 1; i >= 0; i-- {
		c := contexts[i]
		if cmd == "some" {
			node = &SomeExpr{
				Varname:    c.varName,
				ListExpr:   c.listExpr,
				FilterExpr: node,
				textRange:  rng,
			}
		} else {
			node = &EveryExpr{
				Varname:    c.varName,
				ListExpr:   c.listExpr,
				FilterExpr: node,
				textRange:  rng,
			}
		}
	}
	return node, nil
}

func (p *Parser) parseFunDef() (Node, error) {
	rng := p.startTextRange()
	err := p.scanner.Next()
	if err != nil {
		return nil, err
	}

	if !p.CurrentToken().Expect("(") {
		return nil, p.Unexpected("(")
	}
	err = p.scanner.Next()
	if err != nil {
		return nil, err
	}

	// parse var list
	var args []string
	for !p.CurrentToken().Expect(")") {
		argName, err := p.parseName()
		if err != nil {
			return nil, err
		}

		args = append(args, argName)

		if p.CurrentToken().Expect(",") {
			err = p.scanner.Next()
			if err != nil {
				return nil, err
			}
		} else if !p.CurrentToken().Expect(")") {
			return nil, p.Unexpected(")", ",")
		}
	}
	if isdup, name := hasDupName(args); isdup {
		return nil, errors.New(fmt.Sprintf("function arg name '%s' duplicates", name))
	}

	if p.CurrentToken().Expect(")") {
		err = p.scanner.Next()
		if err != nil {
			return nil, err
		}
	}

	exp, err := p.expression()
	if err != nil {
		return nil, err
	}
	rng.End = p.CurrentToken().Pos
	return &FunDef{
		Args:      args,
		Body:      exp,
		textRange: rng,
	}, nil
}
