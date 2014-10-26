package qlparse

import (
	"fmt"
	u "github.com/araddon/gou"
	"strings"
	"unicode"
	"unicode/utf8"
)

var _ = u.EMPTY

// Tokens ---------------------------------------------------------------------

// TokenType identifies the type of lexical tokens.
type TokenType uint16

// token represents a text string returned from the lexer.
type Token struct {
	T TokenType // type
	V string    // value
}

const (
	// List of all TokenTypes
	TokenNil               TokenType = iota // not used
	TokenEOF                                // EOF
	TokenError                              // error occurred; value is text of error
	TokenText                               // plain text
	TokenComment                            // Comment value string
	TokenCommentStart                       // /*
	TokenCommentEnd                         // */
	TokenCommentSingleLine                  // Single Line comment:   -- hello
	TokenCommentHash                        // Single Line comment:  # hello
	// Primitive literals.
	TokenBool
	TokenFloat
	TokenInteger
	TokenString
	TokenList
	TokenMap

	// Logical Evaluation/expression inputs
	TokenStar             // *
	TokenEqual            // =
	TokenNE               // !=
	TokenGE               // >=
	TokenLE               // <=
	TokenGT               // >
	TokenLT               // <
	TokenLeftParenthesis  // (
	TokenRightParenthesis // )
	TokenComma            // ,
	TokenLogicOr          // OR
	TokenLogicAnd         // AND
	TokenIN               // IN
	TokenLike             // LIKE
	TokenNegate           // NOT

	// ql types
	TokenEOS                  // ;
	TokenUdfExpr              // User defined function, or pass through to source
	TokenTable                // table name
	TokenColumn               // column name
	TokenInsert               // insert
	TokenInto                 // into
	TokenUpdate               // update
	TokenSet                  // set
	TokenAs                   // as
	TokenDelete               // delete
	TokenFrom                 // from
	TokenSelect               // select
	TokenSkip                 // skip
	TokenWhere                // where
	TokenGroupBy              // group by
	TokenValues               // values
	TokenValue                // 'some string' string or continous sequence of chars delimited by WHITE SPACE | ' | , | ( | )
	TokenValueWithSingleQuote // '' becomes ' inside the string, parser will need to replace the string
	TokenKey                  // key
	TokenTag                  // tag

)

var (
	// Which Identity Characters are allowed?
	IDENTITY_CHARS = "_."
	// A much more lax identity char set rule
	IDENTITY_LAX_CHARS = "_./ "

	// list of token-name
	TokenNameMap = map[TokenType]string{
		TokenEOF:               "EOF",
		TokenError:             "Error",
		TokenComment:           "Comment",
		TokenCommentStart:      "/*",
		TokenCommentEnd:        "*/",
		TokenCommentHash:       "#",
		TokenCommentSingleLine: "--",
		TokenText:              "Text",
		// Primitive literals.
		TokenBool:    "Bool",
		TokenFloat:   "Float",
		TokenInteger: "Integer",
		TokenString:  "String",
		TokenList:    "List",
		TokenMap:     "Map",
		// Logic, Expressions, Commas etc
		TokenStar:             "Star",
		TokenEqual:            "Equal",
		TokenNE:               "NE",
		TokenGE:               "GE",
		TokenLE:               "LE",
		TokenGT:               "GT",
		TokenLT:               "LT",
		TokenLeftParenthesis:  "LeftParenthesis",
		TokenRightParenthesis: "RightParenthesis",
		TokenComma:            "Comma",
		TokenLogicOr:          "Or",
		TokenLogicAnd:         "And",
		TokenIN:               "IN",
		TokenLike:             "LIKE",
		TokenNegate:           "NOT",
		// Expression
		TokenUdfExpr: "EXPR",
		// QL Keywords, all lower-case
		TokenEOS:                  "EndOfStatement",
		TokenTable:                "table",
		TokenColumn:               "column",
		TokenInsert:               "insert",
		TokenInto:                 "into",
		TokenUpdate:               "update",
		TokenSet:                  "set",
		TokenAs:                   "as",
		TokenDelete:               "delete",
		TokenFrom:                 "from",
		TokenSelect:               "select",
		TokenWhere:                "where",
		TokenGroupBy:              "group by",
		TokenValues:               "values",
		TokenValue:                "value",
		TokenValueWithSingleQuote: "valueWithSingleQuote",
	}
)

// convert to human readable string
func (typ TokenType) String() string {
	s, ok := TokenNameMap[typ]
	if ok {
		return s
	}
	return "not implemented"
}

// Token Emit/Writer

// tokenEmitter accepts tokens found by lexer and allows storage or channel emission
type tokenEmitter interface {
	Emit(t *Token)
}

type tokensStoreEmitter struct {
	idx    int
	tokens []*Token
}

// String converts tokensProducerConsumer to a string.
func (t tokensStoreEmitter) String() string {
	return fmt.Sprintf(
		"tokensProducerConsumer: idx=%d; tokens(%d)=%s",
		t.idx,
		len(t.tokens),
		t.tokens)
}

// Lexer ----------------------------------------------------------------------

const (
	eof        = -1
	leftDelim  = "{"
	rightDelim = "}"
	decDigits  = "0123456789"
	hexDigits  = "0123456789ABCDEF"
)

// StateFn represents the state of the lexer as a function that returns the
// next state.
type StateFn func(*Lexer) StateFn

type NamedStateFn struct {
	Name    string
	StateFn StateFn
}

// newLexer creates a new lexer for the input string.
//
// It is borrowed from the text/template package with minor changes.
func NewLexer(input string) *Lexer {
	// Two tokens of buffering is sufficient for all state functions.
	l := &Lexer{
		input: input,
		state: lexStatement,
		// 200 seems excesive, but since multiple Comments Can be found? before we reach
		// our token, this is needed?
		tokens:  make(chan Token, 1),
		stack:   make([]NamedStateFn, 0, 10),
		dialect: SqlDialect,
	}
	return l
}

// lexer holds the state of the lexical scanning.
//
// Based on the lexer from the "text/template" package.
// See http://www.youtube.com/watch?v=HxaD_trXwRE
type Lexer struct {
	input       string       // the string being scanned.
	state       StateFn      // the next lexing function to enter.
	pos         int          // current position in the input.
	start       int          // start position of this token.
	width       int          // width of last rune read from input.
	emitter     tokenEmitter // hm
	tokens      chan Token   // channel of scanned tokens.
	doubleDelim bool         // flag for tags starting with double braces.
	dialect     *Dialect

	// Due to nested Expressions and evaluation this allows us to descend/ascend
	// during lex
	stack []NamedStateFn
}

// nextToken returns the next token from the input.
func (l *Lexer) NextToken() Token {
	for {
		select {
		case token := <-l.tokens:
			u.Debug("return token")
			return token
		default:
			if l.state == nil && len(l.stack) > 0 {
				l.state = l.pop()
			} else if l.state == nil {
				u.Error("no state? ")
				//panic("no state?")
				return Token{T: TokenEOF, V: ""}
			}
			u.Debugf("calling l.state()")
			l.state = l.state(l)
		}
	}
	panic("not reached")
}

func (l *Lexer) push(name string, state StateFn) {
	//u.LogTracef(u.INFO, "pushed item onto stack: %v", len(l.stack))
	u.Infof("pushed item onto stack: %v  %v", name, len(l.stack))
	l.stack = append(l.stack, NamedStateFn{name, state})
}

func (l *Lexer) pop() StateFn {
	if len(l.stack) == 0 {
		return l.errorf("BUG in lexer: no states to pop.")
	}
	li := len(l.stack) - 1
	last := l.stack[li]
	l.stack = l.stack[0:li]
	u.Infof("popped item off stack:  %v", last.Name)
	return last.StateFn
}

// next returns the next rune in the input.
func (l *Lexer) next() (r rune) {
	if l.pos >= len(l.input) {
		l.width = 0
		return eof
	}
	r, l.width = utf8.DecodeRuneInString(l.input[l.pos:])
	l.pos += l.width
	return r
}

func (l *Lexer) skipX(ct int) {
	for i := 0; i < ct; i++ {
		l.next()
	}
}

// peek returns but does not consume the next rune in the input.
func (l *Lexer) peek() rune {
	r := l.next()
	l.backup()
	return r
}

// lets grab the next word (till whitespace, without consuming)
func (l *Lexer) peekX(x int) string {
	if l.pos+x > len(l.input) {
		return l.input[l.pos:]
	}
	return l.input[l.pos : l.pos+x]
}

// lets grab the next word (till whitespace, without consuming)
func (l *Lexer) peekWord() string {
	word := ""
	for i := 0; i < len(l.input)-l.pos; i++ {
		r, _ := utf8.DecodeRuneInString(l.input[l.pos+i:])
		if unicode.IsSpace(r) || !isAlNumOrPeriod(r) {
			return word
		} else {
			word = word + string(r)
		}
	}
	return word
}

// backup steps back one rune. Can only be called once per call of next.
func (l *Lexer) backup() {
	l.pos -= l.width
}

// have we consumed all input
func (l *Lexer) isEnd() bool {
	if l.pos >= len(l.input) {
		return true
	}
	return false
}

// emit passes an token back to the client.
func (l *Lexer) emit(t TokenType) {
	u.Infof("emit: %s  '%s'", t, l.input[l.start:l.pos])
	l.tokens <- Token{t, l.input[l.start:l.pos]}
	l.start = l.pos
}

// ignore skips over the pending input before this point.
func (l *Lexer) ignore() {
	l.start = l.pos
}

// accept consumes the next rune if it's from the valid set.
func (l *Lexer) accept(valid string) bool {
	if strings.IndexRune(valid, l.next()) >= 0 {
		return true
	}
	l.backup()
	return false
}

// acceptRun consumes a run of runes from the valid set.
func (l *Lexer) acceptRun(valid string) bool {
	pos := l.pos
	for strings.IndexRune(valid, l.next()) >= 0 {
	}
	l.backup()
	return l.pos > pos
}

// Returns current lexeme string.
func (l *Lexer) current() string {
	str := l.input[l.start:l.pos]
	l.start = l.pos
	return str
}

// lets move position to word
func (l *Lexer) consumeWord(word string) {
	// pretty sure the len(word) is valid right?
	l.pos += len(word)
}

// lineNumber reports which line we're on. Doing it this way
// means we don't have to worry about peek double counting.
func (l *Lexer) lineNumber() int {
	return 1 + strings.Count(l.input[:l.pos], "\n")
}

// columnNumber reports which column in the current line we're on.
func (l *Lexer) columnNumber() int {
	n := strings.LastIndex(l.input[:l.pos], "\n")
	if n == -1 {
		n = 0
	}
	return l.pos - n
}

// error returns an error token and terminates the scan by passing
// back a nil pointer that will be the next state, terminating l.nextToken.
func (l *Lexer) errorf(format string, args ...interface{}) StateFn {
	l.tokens <- Token{TokenError, fmt.Sprintf(format, args...)}
	return nil
}

// Skips white space characters in the input.
func (l *Lexer) skipWhiteSpaces() {
	for rune := l.next(); unicode.IsSpace(rune); rune = l.next() {
	}
	l.backup()
	l.ignore()
}

// Scans input and matches against the string.
// Returns true if the expected string was matched.
// expects matchTo to be a lower case string
func (l *Lexer) match(matchTo string, skip int) bool {

	for _, matchRune := range matchTo {
		if skip > 0 {
			skip--
			continue
		}

		nr := l.next()
		//u.Debugf("rune=%s n=%s   %v  %v", string(matchRune), string(nr), matchRune != nr, unicode.ToLower(nr) != matchRune)
		if matchRune != nr && unicode.ToLower(nr) != matchRune {
			//u.Debugf("setting done = false?, ie did not match")
			return false
		}
	}
	// If we finished looking for the match word, and the next item is not
	// whitespace, it means we failed
	if !isWhiteSpace(l.peek()) {
		return false
	}
	u.Debugf("Found match():  %v", matchTo)
	return true
}

// Scans input and tries to match the expected string.
// Returns true if the expected string was matched.
// Does not advance the input if the string was not matched.
//
// NOTE:  this assumes the @val you are trying to match against is LOWER CASE
func (l *Lexer) tryMatch(matchTo string) bool {
	i := 0
	for _, matchRune := range matchTo {
		i++
		nextRune := l.next()
		if unicode.ToLower(nextRune) != matchRune {
			for ; i > 0; i-- {
				l.backup()
			}
			return false
		}
	}
	return true
}

// Emits an error token and terminates the scan
// by passing back a nil ponter that will be the next state
// terminating lexer.next function
func (l *Lexer) errorToken(format string, args ...interface{}) StateFn {
	//fmt.Sprintf(format, args...)
	l.emit(TokenError)
	return nil
}

// non-consuming isExpression
func (l *Lexer) isExpr() bool {
	// Expressions are strings not values
	if r := l.peek(); r == '\'' {
		return false
	} else if isDigit(r) {
		return false
	}
	// Expressions are terminated by either a parenthesis
	// never by spaces
	for i := 0; i < len(l.input)-l.pos; i++ {
		r, _ := utf8.DecodeRuneInString(l.input[l.pos+i:])
		if r == '(' && i > 0 {
			return true
		} else if unicode.IsSpace(r) {
			return false
		} else if !isAlNumOrPeriod(r) {
			return false
		} // else isAlNumOrPeriod so keep looking
	}
	return false
}

// non-consuming check to see if we are about to find next keyword
func (l *Lexer) isNextKeyword() bool {
	word := l.peekWord()
	return strings.ToUpper(word) == "FROM"
}

// non-consuming isIdentity
//  Identities are non-numeric string values that are not quoted
func (l *Lexer) isIdentity() bool {
	// Identity are strings not values
	r := l.peek()
	if r == '\'' {
		return false
	} else if isDigit(r) {
		return false
	} else if isAlpha(r) {
		return true
	}
	return false
}

// lexMatch matches expected tokentype emitting the token on success
// and returning passed state function.
func (l *Lexer) lexMatch(typ TokenType, skip int, fn StateFn) StateFn {
	u.Debugf("lexMatch   t=%s peek=%s", typ, l.peekWord())
	if l.match(typ.String(), skip) {
		u.Debugf("found match: %s   %v", typ, fn)
		l.emit(typ)
		return fn
	}
	u.Error("unexpected token", typ)
	return l.errorToken("Unexpected token:" + l.current())
}

// lexer to match expected value returns with args of
//   @matchState state function if match
//   @noMatchState state function if no match
func (l *Lexer) lexIfElseMatch(typ TokenType, matchState StateFn, noMatchState StateFn) StateFn {
	l.skipWhiteSpaces()
	if l.tryMatch(typ.String()) {
		l.emit(typ)
		return matchState
	}
	return noMatchState
}

// State functions ------------------------------------------------------------

// lexStatement scans until finding an opening keyword
//   or comment, skipping whitespace
func lexStatement(l *Lexer) StateFn {

	l.skipWhiteSpaces()

	// ensure we have consumed all comments
	r := l.peek()
	//u.Debugf("lexStatement? %s", string(r))
	// TODO:  use lexToplevelIdentifier() here where top-level words are DIALECT
	switch r {
	case '/', '-', '#': // comments
		//u.Debugf("found comment?   %s", string(r))
		return lexComment(l, lexStatement)
		//u.Infof("After lex comment?")
	case 's', 'S': // select
		//u.Debugf("found select?? %s", string(l.peek()))
		// All select statements must have FROM after SELECT
		l.push("from keyword", makeStateFnKeyword(TokenFrom, lexSqlFromTable))
		return l.lexMatch(TokenSelect, 0, lexColumnOrComma)
	// case 'i': // insert
	// 	return l.lexMatch(tokenTypeSqlInsert, "INSERT", 1, lexSqlInsertInto)
	// case 'd': // delete
	// 	return l.lexMatch(tokenTypeSqlDelete, "DELETE", 1, lexSqlFrom)
	// 	return lexCommandP(l)
	default:
		u.Errorf("not found?    r=%s", string(l.peek()))
	}

	// Correctly reached EOF.
	if l.pos > l.start {
		l.emit(TokenText)
	}
	l.emit(TokenEOF)
	return nil
}

// lexValue looks in input for a sql value, then emits token on
// success and then returns passed in next state func
func (l *Lexer) lexValue() StateFn {

	u.Debugf("in lexValue: ")
	l.skipWhiteSpaces()
	if l.isEnd() {
		return l.errorToken("expected value but got EOF")
	}
	rune := l.next()
	typ := TokenValue
	if rune == ')' {
		// Whoops
		return nil
	}

	// quoted string
	if rune == '\'' || rune == '"' {
		l.ignore() // consume the quote mark
		for rune = l.next(); ; rune = l.next() {
			u.Debugf("lexValue rune=%v  end?%v", string(rune), rune == eof)
			if rune == '\'' || rune == '"' {
				if !l.isEnd() {
					rune = l.next()
					// check for '''
					if rune == '\'' || rune == '"' {
						typ = TokenValueWithSingleQuote
					} else {
						// since we read lookahead after single quote that ends the string
						// for lookahead
						l.backup()
						// for single quote which is not part of the value
						l.backup()
						l.emit(typ)
						// now ignore that single quote
						l.next()
						l.ignore()
						return nil
					}
				} else {
					// at the very end
					l.backup()
					l.emit(typ)
					l.next()
					return nil
				}
			}
			if rune == 0 {
				return l.errorToken("string value was not delimited")
			}
		}
		// value
	} else {
		for rune = l.next(); !isWhiteSpace(rune) && rune != ',' && rune != ')'; rune = l.next() {
		}
		l.backup()
		l.emit(typ)
	}
	return nil
}

func lexValue(l *Lexer) StateFn {
	l.lexValue()
	return nil
}

// look for either an Expression or Identity
func lexExpressionOrIdentity(l *Lexer) StateFn {

	l.skipWhiteSpaces()

	peek := l.peekWord()
	peekChar := l.peek()
	u.Debugf("in lexExpressionOrIdentity %v:%v", string(peekChar), string(peek))
	//  Expressions end in Parens:     LOWER(item)
	if l.isExpr() {
		return lexExpressionIdentifier(l, lexExpression)
	} else if l.isIdentity() {
		// Non Expressions are Identities, or Columns
		u.Warnf("in expr is identity? %s", l.peekWord())
		// by passing nil here, we are going to go back to Pull items off stack)
		l.lexIdentifier(TokenColumn, nil)
	} else {
		u.Warnf("lexExpressionOrIdentity ??? '%v'", peek)
		return l.lexValue()
	}

	return nil
}

// lex Expression looks for an expression, identified by parenthesis
//
//    lower(xyz)    // the left parenthesis identifies it as Expression
func lexExpression(l *Lexer) StateFn {

	// first rune has to be valid unicode letter
	firstChar := l.next()
	u.Debugf("lexExpression:  %v", string(firstChar))
	if firstChar != '(' {
		u.Errorf("bad expression? %v", string(firstChar))
		return l.errorToken("expression must begin with a paren: ( " + string(l.input[l.start:l.pos]))
	}
	l.emit(TokenLeftParenthesis)
	//l.push("lexExpressionEnd", lexExpressionEnd)
	return lexColumnOrComma
}

// lex expression identity keyword
func lexExpressionIdentifier(l *Lexer, nextFn StateFn) StateFn {

	l.skipWhiteSpaces()

	// first rune has to be valid unicode letter
	firstChar := l.next()

	if !unicode.IsLetter(firstChar) {
		u.Warnf("lexExpressionIdentifier couldnt find expression idenity?  %v stack=%v", string(firstChar), len(l.stack))
		return l.errorToken("identifier must begin with a letter " + string(l.input[l.start:l.pos]))
	}
	for rune := l.next(); isIdentifierRune(rune); rune = l.next() {
		// iterate until we find non-identifer character
	}
	l.backup() // back up one character
	l.emit(TokenUdfExpr)
	return nextFn
}

// lexIdentifier scans and finds named things (tables, columns)
// finding valid identifier
//
//   TODO: dialect controls escaping/quoting techniques
//
//  [name]       select [first name] from usertable;
//  'name'       select 'user' from usertable;
//   name        select first_name from usertable;
//
func (l *Lexer) lexIdentifier(typ TokenType, nextFn StateFn) StateFn {

	l.skipWhiteSpaces()

	wasQouted := false
	// first rune has to be valid unicode letter
	firstChar := l.next()
	u.Debugf("lexIdentifier:   %s is='? %v", string(firstChar), firstChar == '\'')
	switch firstChar {
	case '[', '\'':
		l.ignore()
		nextChar := l.next()
		if !unicode.IsLetter(nextChar) {
			u.Warnf("aborting lexIdentifier: %v", string(nextChar))
			return l.errorToken("identifier must begin with a letter " + l.input[l.start:l.pos])
		}
		for nextChar = l.next(); isLaxIdentifierRune(nextChar); nextChar = l.next() {

		}
		// iterate until we find non-identifier, then make sure
		if firstChar == '[' && nextChar == ']' {
			// valid
		} else if firstChar == '\'' && nextChar == '\'' {
			// also valid
		} else {
			u.Errorf("unexpected character in identifier?  %v", string(nextChar))
			return l.errorToken("unexpected character in identifier:  " + string(nextChar))
		}
		wasQouted = true
		l.backup()
		u.Debugf("quoted?:   %v  ", l.input[l.start:l.pos])
	default:
		if !unicode.IsLetter(firstChar) {
			u.Warnf("aborting lexIdentifier: %v", string(firstChar))
			return l.errorToken("identifier must begin with a letter " + string(l.input[l.start:l.pos]))
		}
		for rune := l.next(); isIdentifierRune(rune); rune = l.next() {
			// iterate until we find non-identifer character
		}
		l.backup()
	}
	u.Debugf("about to emit: %#v", typ)
	l.emit(typ)
	if wasQouted {
		// need to skip last character bc it was quoted
		l.next()
		l.ignore()
	}

	// TODO:  replace this AS

	// l.skipWhiteSpaces()
	// word := l.peekWord()
	// if strings.ToUpper(word) == "AS" {
	// 	l.skipX(2)
	// 	l.emit(TokenAs)
	// 	return l.lexIdentifier(TokenColumn, nextState)
	// }
	u.Debugf("about to return:  %v", nextFn)
	return nextFn // pop up to parent
}

func makeStateFnKeyword(tokenType TokenType, matchFn StateFn) StateFn {
	return func(l *Lexer) StateFn {
		l.skipWhiteSpaces()
		u.Debug("lex keyword: %v", tokenType)
		return l.lexMatch(TokenFrom, 0, matchFn)
	}
}

// func makeStateFnOptionalKeyword(tokenType TokenType, matchFn, noMatchFn StateFn) StateFn {
// 	return func(l *Lexer) StateFn {
// 		l.skipWhiteSpaces()
// 		u.Debug("lex keyword: %v", tokenType)
// 		return l.lexIfElseMatch(TokenFrom, 0, matchFn, noMatchFn)
// 	}
// }

func lexSqlFromTable(l *Lexer) StateFn {
	u.Debugf("lexSqlFromTable:  %s", l.peekWord())
	return l.lexIdentifier(TokenTable, lexSqlWhere)
}

func lexSqlEndOfStatement(l *Lexer) StateFn {
	l.skipWhiteSpaces()
	r := l.next()
	u.Debugf("sqlend of statement  %s", string(r))
	if r == ';' {
		l.emit(TokenEOS)
	}
	if l.isEnd() {
		return nil
	}
	return l.errorToken("Unexpected token:" + l.current())
}

func lexSqlWhere(l *Lexer) StateFn {
	u.Debugf("in lexSqlWhere")
	return l.lexIfElseMatch(TokenWhere, lexSqlWhereColumn, lexGroupBy)
}

func lexSqlWhereColumn(l *Lexer) StateFn {
	return l.lexIdentifier(TokenColumn, lexSqlWhereColumnExpr)
}

func lexSqlWhereCommaOrLogicOrNext(l *Lexer) StateFn {

	l.skipWhiteSpaces()

	r := l.next()
	//u.LogTracef(u.INFO, "lexSqlWhereCommaOrLogicOrNext: %v", string(r))
	switch r {
	case '(':
		l.emit(TokenLeftParenthesis)
		l.skipWhiteSpaces()
		u.Error("Found ( parenthesis")
	case ')':
		l.emit(TokenRightParenthesis)
		l.skipWhiteSpaces()
		u.Error("Found ) parenthesis")
	case ',':
		l.emit(TokenComma)
		return lexSqlWhereColumn
	default:
		l.backup()
	}

	word := l.peekWord()
	word = strings.ToUpper(word)
	u.Debugf("sqlcommaorlogic:  word=%s", word)
	switch word {
	case "":
		u.Warnf("word = groupby?  %v", word)
		return lexGroupBy
	case "OR": // OR
		l.skipX(2)
		l.emit(TokenLogicOr)
		return lexSqlWhereColumn
	case "AND": // AND
		l.skipX(3)
		l.backup()
		l.emit(TokenLogicAnd)
		return lexSqlWhereColumn
	default:
		u.Infof("Did not find right expression ?  %s len=%d", word, len(word))
		l.backup()
	}

	// Since we do not have another Where expr, then go to next
	return lexGroupBy
}

// Handles within a Where Clause the Start of an expression, when in a WHERE
//
//  WHERE (colx = y OR colb = b)
//  WHERE cola = 'a5'
//  WHERE cola != "a5"
//  WHERE REPLACE(cola,"stuff") != "hello"
//  WHERE cola IN (1,2,3)
//  WHERE cola LIKE "abc"
//
func lexSqlWhereColumnExpr(l *Lexer) StateFn {
	u.Debug("lexSqlWhereColumnExpr")
	l.skipWhiteSpaces()
	r := l.next()
	switch r {
	case '!':
		if r2 := l.peek(); r2 == '=' {
			l.next()
			l.emit(TokenNE)
		} else {
			u.Error("Found ! without equal")
			return nil
		}
	case '=':
		l.emit(TokenEqual)
	case '>':
		if r2 := l.peek(); r2 == '=' {
			l.next()
			l.emit(TokenGE)
		}
		l.emit(TokenGT)
	case '<':
		if r2 := l.peek(); r2 == '=' {
			l.next()
			l.emit(TokenLE)
		}
	default:
		l.backup()
		word := l.peekWord()
		word = strings.ToUpper(word)
		u.Debugf("looking for operator:  word=%s", word)
		switch word {
		case "IN": // IN
			l.skipX(2)
			l.emit(TokenIN)
			l.push("lexSqlWhereCommaOrLogicOrNext", lexSqlWhereCommaOrLogicOrNext)
			l.push("lexColumnOrComma", lexColumnOrComma)
			return nil
			// return lexCommaValues(l, func(l *Lexer) StateFn {
			// 	u.Debug("in IN lex return?")
			// 	return lexSqlWhereCommaOrLogicOrNext
			// })
		case "LIKE": // LIKE
			l.skipX(4)
			l.emit(TokenLike)
		default:
			u.Infof("Did not find right expression ?  %s", word)
		}
	}
	//u.LogTracef(u.WARN, "hmmmmmmm")
	l.push("lexSqlWhereCommaOrLogicOrNext", lexSqlWhereCommaOrLogicOrNext)
	l.push("lexValue", lexValue)
	return nil
}

func lexGroupBy(l *Lexer) StateFn {
	return l.lexIfElseMatch(TokenGroupBy, lexSqlGroupByColumns, lexSqlEndOfStatement)
}

func lexSqlGroupByColumns(l *Lexer) StateFn {
	u.LogTracef(u.ERROR, "group by not implemented")

	return lexColumnOrComma
}

//  Expression or Column, most noteable used for
//     SELECT [    ,[ ]] FROM
//     WHERE [x, [y]]
//     GROUP BY x, [y]
//
//  where a column can be
//       REPLACE(LOWER(x),"xyz")
//
//  and multiple columns separated by commas
//      LOWER(cola), UPPER(colb)
//
//  and columns can be grouped by parenthesis (for WHERE)
func lexColumnOrComma(l *Lexer) StateFn {

	// as we descend into Expressions, we are going to use push/pop

	l.skipWhiteSpaces()

	r := l.next()
	u.Debugf("in lexColumnOrComma:  '%s'", string(r))
	if unicode.ToLower(r) == 'a' {

		if p2 := l.peekX(2); strings.ToLower(p2) == "s " {
			// AS xyz
			l.next()
			l.emit(TokenAs)
			return lexValue
		}
	}
	switch r {
	case '(':
		// begin paren denoting logical grouping
		// TODO:  this isn't valid for SELECT, only WHERE?
		l.emit(TokenLeftParenthesis)
		return lexColumnOrComma
	case ')':
		// WE have an end paren end of this column/comma
		l.emit(TokenRightParenthesis)
		return nil
	case ',': // go to next column
		l.emit(TokenComma)
		u.Debugf("just emitted comma?")
		return lexColumnOrComma
	case '*':
		l.emit(TokenStar)
		return nil
	default:
		// So, not comma, * so either is expression or Identity
		l.backup()
		if l.isNextKeyword() {
			u.Warnf("found keyword while looking for column? %v", string(r))
			return nil
		}
		if len(l.stack) < 10 {
			l.push("columnorcomma", lexColumnOrComma)
		}
		u.Debugf("in col or comma sending to expression or identity")
		return lexExpressionOrIdentity
	}

	u.Warnf("exit lexColumnOrComma")
	return nil
}

// lexComment looks for valid comments which are
//
//  /* hello */
//  //  hello
//  -- hello
//  # hello
func lexComment(l *Lexer, nextFn StateFn) StateFn {
	//u.Debugf("checking comment: '%s' ", l.input[l.pos:l.pos+2])
	if strings.HasPrefix(l.input[l.pos:], "/*") {
		return lexMultilineCmt(l, nextFn)
	} else if strings.HasPrefix(l.input[l.pos:], "//") {
		//u.Debugf("found single line comment:  // ")
		return lexSingleLineCmt(l, nextFn)
	} else if strings.HasPrefix(l.input[l.pos:], "--") {
		//u.Debugf("found single line comment:  -- ")
		return lexSingleLineCmt(l, nextFn)
	} else if strings.HasPrefix(l.input[l.pos:], "#") {
		//u.Debugf("found single line comment:  # ")
		return lexSingleLineCmt(l, nextFn)
	}
	u.Warn("What, no comment after all?")
	return nil
}

func lexMultilineCmt(l *Lexer, nextFn StateFn) StateFn {
	// Consume opening "/*"
	l.next()
	l.next()
	for {
		if strings.HasPrefix(l.input[l.pos:], "*/") {
			break
		}
		r := l.next()
		if eof == r {
			panic("Unexpected end of file inside multiline comment")
		}
	}
	// Consume trailing "*/"
	l.next()
	l.next()
	l.emit(TokenComment)

	return nextFn
}

func lexSingleLineCmt(l *Lexer, nextFn StateFn) StateFn {
	// Consume opening "//" or -- or #
	r := l.next()
	if r == '-' || r == '/' {
		l.next()
	} // else if r == # we only need one

	for {
		r = l.next()
		if r == '\n' || r == eof {
			l.backup()
			break
		}
	}
	l.emit(TokenComment)
	return nextFn
}

func lexLeftParen(l *Lexer, nextFn StateFn) StateFn {
	// Consume opening "//" or -- or #
	r := l.next()
	if r == '(' {
		l.emit(TokenComment)
		return nextFn
	}
	return nil
}

// lexNumber scans a number: a float or integer (which can be decimal or hex).
func lexNumber(l *Lexer) StateFn {
	typ, ok := scanNumber(l)
	if !ok {
		return l.errorf("bad number syntax: %q", l.input[l.start:l.pos])
	}
	// Emits tokenFloat or tokenInteger.
	l.emit(typ)
	return nil
}

// scan for a number
//
// It returns the scanned tokenType (tokenFloat or tokenInteger) and a flag
// indicating if an error was found.
//
// Floats must be in decimal and must either:
//
//     - Have digits both before and after the decimal point (both can be
//       a single 0), e.g. 0.5, -100.0, or
//     - Have a lower-case e that represents scientific notation,
//       e.g. -3e-3, 6.02e23.
//
// Integers can be:
//
//     - decimal (e.g. -827)
//     - hexadecimal (must begin with 0x and must use capital A-F,
//       e.g. 0x1A2B).
func scanNumber(l *Lexer) (typ TokenType, ok bool) {
	typ = TokenInteger
	// Optional leading sign.
	hasSign := l.accept("+-")
	if l.input[l.pos:l.pos+2] == "0x" {
		// Hexadecimal.
		if hasSign {
			// No signs for hexadecimals.
			return
		}
		l.acceptRun("0x")
		if !l.acceptRun(hexDigits) {
			// Requires at least one digit.
			return
		}
		if l.accept(".") {
			// No dots for hexadecimals.
			return
		}
	} else {
		// Decimal.
		if !l.acceptRun(decDigits) {
			// Requires at least one digit.
			return
		}
		if l.accept(".") {
			// Float.
			if !l.acceptRun(decDigits) {
				// Requires a digit after the dot.
				return
			}
			typ = TokenFloat
		} else {
			if (!hasSign && l.input[l.start] == '0') ||
				(hasSign && l.input[l.start+1] == '0') {
				// Integers can't start with 0.
				return
			}
		}
		if l.accept("e") {
			l.accept("+-")
			if !l.acceptRun(decDigits) {
				// A digit is required after the scientific notation.
				return
			}
			typ = TokenFloat
		}
	}
	// Next thing must not be alphanumeric.
	if isAlNum(l.peek()) {
		l.next()
		return
	}
	ok = true
	return
}

// Helpers --------------------------------------------------------------------

// is Alpha Numeric reports whether r is an alphabetic, digit, or underscore.
func isAlNum(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// is Alpha reports whether r is an alphabetic, or underscore or period
func isAlpha(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || r == '.'
}

// is Alpha Numeric reports whether r is an alphabetic, digit, or underscore, or period
func isAlNumOrPeriod(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.'
}

func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}
func isWhiteSpace(r rune) bool {
	switch r {
	case '\r', '\n', '\t', ' ':
		return true
	}
	return false
}

// Is the given rune valid in an identifier?
func isIdentCh(r rune) bool {
	switch {
	case isAlNum(r):
		return true
	case r == '_':
		return true
	}
	return false
}

func isIdentifierRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	for _, allowedRune := range IDENTITY_CHARS {
		if allowedRune == r {
			return true
		}
	}
	return false
}

func isLaxIdentifierRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	for _, allowedRune := range IDENTITY_LAX_CHARS {
		if allowedRune == r {
			return true
		}
	}
	return false
}
