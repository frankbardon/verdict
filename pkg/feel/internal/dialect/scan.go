package dialect

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	TokenEOF               = "eof"
	TokenSpace             = "space"
	TokenCommentSingleLine = "comment/singleline"
	TokenCommentMultiline  = "comment/multiline"

	TokenName = "name"
	//TokenFuncall  = "funcall"
	TokenTemporal = "temporal"
	TokenString   = "string"

	TokenKeyword = "keyword"
	TokenNumber  = "number"
)

type tokenMatcher struct {
	token string
	reg   *regexp.Regexp
	// scan, when set, replaces reg: it returns the token's text at the start
	// of its argument, or "" when the token does not start there.
	scan func(string) string
}

func match(token string, regstr string) tokenMatcher {
	if regstr == "" {
		return tokenMatcher{token: token}
	}
	return tokenMatcher{
		token: token,
		reg:   regexp.MustCompile("^" + regstr),
	}
}

var tokenMatchers = []tokenMatcher{
	match(TokenSpace, `\s+`),
	match(TokenCommentSingleLine, `//.*\n`),
	match(TokenCommentMultiline, `\/\*(.|\n)*\*\/`),

	{token: TokenKeyword, scan: scanKeyword},

	match(TokenTemporal, `@"(\\.|[^"])*"`),
	match(TokenString, `"(\\.|[^"])*"`),
	// VERDICT DIVERGENCE: a number literal is unsigned. Upstream folded a
	// leading `-` into the literal regardless of what preceded it, so `x-1`
	// scanned as `x` followed by the literal `-1` and evaluated to -1, and
	// `1..n-1` was a syntax error. The minus is always its own token; the
	// parser's negationOp turns a leading one into a Negation and addOrSubOp
	// takes one after an operand as subtraction (DMN 1.5 grammar rules 4.a, 4.d).
	match(TokenNumber, `[0-9]+(\.[0-9]+)?\b`),

	{token: "?", scan: scanInputRef},
	match("..", ""),
	match(".", ""),
	match(",", ""),
	match(";", ""),

	match(">=", ""),
	match(">", ""),

	match("<=", ""),
	match("<", ""),

	match("!=", ""),
	match("!", ""),

	match("=", ""),

	match("(", ""),
	match(")", ""),
	match("[", ""),
	match("]", ""),
	match("{", ""),
	match("}", ""),

	match(":=", ""),
	match(":", ""),

	match("+", ""),
	match("-", ""),
	// VERDICT DIVERGENCE: `**` (exponentiation) is matched before `*`.
	match("**", ""),
	match("*", ""),
	match("/", ""),
	match("%", ""),

	// VERDICT DIVERGENCE: upstream's continuation class carried a stray `}`
	// (`\p{Greek}}`), so a name ran on through a closing brace: `{v: a}` scanned
	// `a}` as one name and the context literal never closed. A `}` is never a
	// name character — DMN 1.5 §10.3.1.2 grammar rules 27-30 (name start and
	// part characters) do not admit it — and rule 59 lets a context entry's
	// value be any expression, a bare name or path included.
	//
	// VERDICT DIVERGENCE: upstream's class admitted only ASCII letters and the
	// Han, Greek, Bopomofo and Hangul scripts, so `Prénom`, `Größe`, `имя` and
	// `Approved?` could not be written. A name is now scanned by the character
	// ranges of rules 28 and 29; see scanName.
	{token: TokenName, scan: scanName},
}

// keywordPattern matches a reserved word at the start of the input. Its `\b`
// is ASCII-only, so scanKeyword also checks the character that follows.
var keywordPattern = regexp.MustCompile(`^\b(true|false|and|or|null|function|if|then|else|loop|for|some|every|in|return|satisfies)\b`)

// scanKeyword matches a reserved word that is not the start of a longer name.
//
// VERDICT DIVERGENCE: upstream ended a keyword at RE2's ASCII word boundary,
// which a non-ASCII letter or `?` also satisfies, so `inné` would scan as the
// keyword `in` followed by the name `né`. A keyword followed by a name part
// character (DMN 1.5 §10.3.1.2 grammar rule 29) or an in-name symbol is the
// start of a name instead: `in?`, `truë` and `forêt` are names.
func scanKeyword(s string) string {
	kw := keywordPattern.FindString(s)
	if kw == "" || continuesName(s[len(kw):]) {
		return ""
	}
	return kw
}

// scanInputRef matches the unary-test input `?` (DMN 1.5 §10.3.1.2, the
// positive unary test of grammar rule 13) when it stands alone. A `?`
// followed by a name character begins a name instead (rule 28 makes `?` a
// name start char), so `?x` is a name and `? > 5` is still a test of the
// input.
func scanInputRef(s string) string {
	if !strings.HasPrefix(s, "?") || continuesName(s[1:]) {
		return ""
	}
	return "?"
}

// scanName matches a name token: a name start char followed by any run of
// name part chars and in-name symbols.
//
// DMN 1.5 §10.3.1.2 grammar rules 25-30:
//
//	(25) name = name start , { name part | additional name symbols } ;
//	(26) name start = name start char, { name part char } ;
//	(27) name part = name part char , { name part char } ;
//	(28) name start char = "?" | [A-Z] | "_" | [a-z] | [\uC0-\uD6] | ... ;
//	(29) name part char = name start char | digit | \uB7 | [\u0300-\u036F] | [\u203F-\u2040] ;
//	(30) additional name symbols = "." | "/" | "-" | "’" | "+" | "*" ;
//
// The spec defines a name as a sequence of tokens and resolves the symbols of
// rule 30 against the names in scope, so `Income+Expenses` is a name only
// where one is bound. This scanner has no scope, and `.`, `/`, `-`, `+` and
// `*` are also FEEL's path and arithmetic operators, so it keeps them as
// operators: `a-b` is a subtraction, `a.b` a path. The apostrophe, `’` or
// `'`, has no other meaning in FEEL and is taken inside a name token
// (`Driver’s Age`), never at its start. The upstream extension `$` is kept as
// a start and part char.
func scanName(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if !isNameStartChar(r, n) {
		return ""
	}
	i := n
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !isNamePartChar(r, n) && !isNameSymbolChar(r) {
			break
		}
		i += n
	}
	return s[:i]
}

// continuesName reports whether s starts with a character that may follow
// the first character of a name.
func continuesName(s string) bool {
	r, n := utf8.DecodeRuneInString(s)
	return n > 0 && (isNamePartChar(r, n) || isNameSymbolChar(r))
}

// nameStartRanges are the non-ASCII ranges of grammar rule 28, which follow
// XML 1.0's NameStartChar.
var nameStartRanges = [...][2]rune{
	{0xC0, 0xD6}, {0xD8, 0xF6}, {0xF8, 0x2FF}, {0x370, 0x37D},
	{0x37F, 0x1FFF}, {0x200C, 0x200D}, {0x2070, 0x218F}, {0x2C00, 0x2FEF},
	{0x3001, 0xD7FF}, {0xF900, 0xFDCF}, {0xFDF0, 0xFFFD}, {0x10000, 0xEFFFF},
}

// isNameStartChar reports whether r, decoded from n bytes, is a name start
// char of grammar rule 28 (or `$`). A byte that is not valid UTF-8 decodes to
// U+FFFD, which rule 28 admits, so it is rejected by its width.
func isNameStartChar(r rune, n int) bool {
	switch {
	case r == utf8.RuneError && n <= 1:
		return false
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', r == '_', r == '?', r == '$':
		return true
	case r < 0xC0:
		return false
	}
	for _, rg := range nameStartRanges {
		if r < rg[0] {
			return false
		}
		if r <= rg[1] {
			return true
		}
	}
	return false
}

// isNamePartChar reports whether r is a name part char of grammar rule 29.
func isNamePartChar(r rune, n int) bool {
	return isNameStartChar(r, n) || '0' <= r && r <= '9' || r == 0xB7 ||
		0x300 <= r && r <= 0x36F || 0x203F <= r && r <= 0x2040
}

// isNameSymbolChar reports whether r is one of grammar rule 30's additional
// name symbols that this scanner takes inside a name token: the apostrophe,
// `’` (as the spec prints it) or `'`.
func isNameSymbolChar(r rune) bool {
	return r == '\u2019' || r == '\''
}

// ScanPosition is a 0-based row and column in the scanned text; Column counts
// characters (runes), not bytes.
type ScanPosition struct {
	Row    int
	Column int
}

// String is the position as a modeller reads it: 1-based, "line 2, column 3".
//
// VERDICT DIVERGENCE: upstream printed the raw 0-based pair ("at 0 0"), which
// read as an unset position for an error at the start of an expression and
// was off by one everywhere else.
func (pos ScanPosition) String() string {
	return fmt.Sprintf("line %d, column %d", pos.Row+1, pos.Column+1)
}

type ScannerToken struct {
	Kind  string
	Value string
	Pos   ScanPosition
}

var opTokens = map[string]bool{
	"+":  true,
	"-":  true,
	"*":  true,
	"**": true,
	"/":  true,
	"%":  true,
}

func (token ScannerToken) IsOp() bool {
	if isOp, ok := opTokens[token.Kind]; ok {
		return isOp
	}
	return false
}

func (token ScannerToken) Expect(tokenKinds ...string) bool {
	for _, kind := range tokenKinds {
		if token.Kind == kind {
			return true
		}
	}
	return false
}

func (token ScannerToken) ExpectKeywords(words ...string) bool {
	if token.Kind != TokenKeyword {
		return false
	}
	for _, kw := range words {
		if token.Value == kw {
			return true
		}
	}
	return false
}

type Scanner struct {
	input string
	rest  string

	currentToken ScannerToken

	Pos   ScanPosition
	Eaten int
}

func NewScanner(input string) *Scanner {
	return &Scanner{
		input: input,
		rest:  input,
	}
}

// Tokens Find all tokens
func (scanner *Scanner) Tokens() ([]ScannerToken, error) {
	err := scanner.Next()
	if err != nil {
		return nil, err
	}
	var arr []ScannerToken
	for scanner.currentToken.Kind != TokenEOF {
		// fmt.Printf("scanner current token %q, rest %q\n", scanner.currentToken.Kind, scanner.rest)
		arr = append(arr, scanner.currentToken)
		err := scanner.Next()
		if err != nil {
			return nil, err
		}
	}
	return arr, nil
}

func (scanner Scanner) Current() ScannerToken {
	return scanner.currentToken
}

func (scanner *Scanner) goAhead(matched string) {
	// find \n
	lastIndexOfLn := strings.LastIndex(matched, "\n")
	if lastIndexOfLn >= 0 {
		// found \n
		scanner.Pos.Column = utf8.RuneCountInString(matched[lastIndexOfLn+1:])
		scanner.Pos.Row += strings.Count(matched, "\n")
	} else {
		// VERDICT DIVERGENCE: columns count characters, not bytes, so a
		// position after a non-ASCII name matches what an editor shows.
		scanner.Pos.Column += utf8.RuneCountInString(matched)
	}
	scanner.rest = scanner.rest[len(matched):]
	scanner.Eaten += len(matched)
}

func (scanner *Scanner) Next() error {
	if scanner.currentToken.Kind == TokenEOF {
		return errors.New("EOF met")
	} else if scanner.rest == "" {
		scanner.currentToken = ScannerToken{Kind: TokenEOF, Pos: scanner.Pos}
		return nil
	}

	for _, matcher := range tokenMatchers {
		matched := ""
		if matcher.scan != nil {
			matched = matcher.scan(scanner.rest)
		} else if matcher.reg == nil {
			if strings.HasPrefix(scanner.rest, matcher.token) {
				matched = matcher.token
			}
		} else {
			matched = matcher.reg.FindString(scanner.rest)
		}
		if matched != "" {
			// find a matching
			if matcher.token == TokenSpace || matcher.token == TokenCommentMultiline || matcher.token == TokenCommentSingleLine {
				scanner.goAhead(matched)
				return scanner.Next()
			} else {
				scanner.currentToken = ScannerToken{
					Kind:  matcher.token,
					Value: matched,
					Pos:   scanner.Pos,
				}
				scanner.goAhead(matched)
				return nil
			}
		}
	}

	if scanner.rest == "" {
		scanner.currentToken = ScannerToken{Kind: TokenEOF, Pos: scanner.Pos}
		return nil
	} else {
		return NewErrBadInput(scanner.Pos.Row, scanner.Pos.Column, scanner.rest)
	}
}
