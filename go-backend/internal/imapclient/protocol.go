package imapclient

// The IMAP response tokenizer of Step 15.
//
// Step 14 got away with a line oriented parser: the greeting, CAPABILITY and
// the authentication replies are flat lines of atoms. The folder commands need
// the full RFC 3501 data model (parenthesized lists, quoted strings, literals
// and NIL), so this file adds the token tree and the literal aware line reader
// on top of the existing readLine().
//
// The resulting response items line up with the PHP ResponseList indexes used
// by MailSo: values[i] is what the PHP code reads as ResponseList[i+1].

import (
	"io"
	"strconv"
	"strings"
)

// tokenKind classifies one parsed IMAP data token.
type tokenKind int

const (
	// tokenAtom is a bare atom ("UIDNEXT", `\Seen`, "123").
	tokenAtom tokenKind = iota
	// tokenString is a quoted string or a literal.
	tokenString
	// tokenNil is the NIL atom, which PHP reads as null.
	tokenNil
	// tokenList is a parenthesized list.
	tokenList
	// tokenClose is the ")" that terminates a list. It never escapes the parser.
	tokenClose
)

// token is one IMAP data item.
type token struct {
	kind tokenKind
	text string
	list []token
}

// textValue is the string value of an atom or a string token.
func (t token) textValue() (string, bool) {
	if tokenAtom == t.kind || tokenString == t.kind {
		return t.text, true
	}
	return "", false
}

// lineSegment is one raw chunk of a protocol line: either text or the bytes of
// an IMAP literal.
type lineSegment struct {
	text    string
	literal bool
}

// readLogicalLine reads one protocol line with its literals resolved.
//
// A literal is announced as "{<size>}" at the end of the line; the following
// <size> bytes are the value and the rest of the line continues right after
// them, which may announce another literal.
func (c *Client) readLogicalLine() ([]lineSegment, error) {
	segments := make([]lineSegment, 0, 2)

	for {
		line, err := c.readLine()
		if nil != err {
			return segments, err
		}

		size, suffix, found := literalAnnouncement(line)
		if !found {
			return append(segments, lineSegment{text: line}), nil
		}

		segments = append(segments, lineSegment{text: line[:len(line)-suffix]})
		if 0 == size {
			continue
		}
		if nil == c.reader {
			return segments, errNotConnected
		}

		buffer := make([]byte, size)
		c.setDeadline()
		if _, err := io.ReadFull(c.reader, buffer); nil != err {
			return segments, err
		}
		segments = append(segments, lineSegment{text: string(buffer), literal: true})
	}
}

// literalAnnouncement reports the literal size when line ends with "{<size>}"
// (or the LITERAL+ form "{<size>+}") and the length of that announcement.
func literalAnnouncement(line string) (size, suffix int, found bool) {
	if !strings.HasSuffix(line, "}") {
		return 0, 0, false
	}

	open := strings.LastIndexByte(line, '{')
	if 0 > open {
		return 0, 0, false
	}

	body := strings.TrimSuffix(line[open+1:len(line)-1], "+")
	number, err := strconv.Atoi(body)
	if nil != err || 0 > number {
		return 0, 0, false
	}

	return number, len(line) - open, true
}

// responseParser walks the tokens of one logical line.
type responseParser struct {
	segments []lineSegment
	index    int
	offset   int
}

// newResponseParser starts a parser at the first segment.
func newResponseParser(segments []lineSegment) *responseParser {
	return &responseParser{segments: segments}
}

// parseText parses a single text chunk (used for response codes).
func parseText(text string) *responseParser {
	return newResponseParser([]lineSegment{{text: text}})
}

// next returns the next token; ok is false at the end of the line.
func (p *responseParser) next() (token, bool) {
	for p.index < len(p.segments) {
		segment := p.segments[p.index]

		if segment.literal {
			p.index++
			p.offset = 0
			return token{kind: tokenString, text: segment.text}, true
		}

		for p.offset < len(segment.text) &&
			(' ' == segment.text[p.offset] || '\t' == segment.text[p.offset]) {
			p.offset++
		}
		if p.offset >= len(segment.text) {
			p.index++
			p.offset = 0
			continue
		}

		return p.readToken()
	}

	return token{}, false
}

// readToken consumes one token at the current position.
func (p *responseParser) readToken() (token, bool) {
	text := p.segments[p.index].text

	switch text[p.offset] {
	case '(':
		p.offset++
		items := make([]token, 0, 4)
		for {
			next, ok := p.next()
			if !ok || tokenClose == next.kind {
				break
			}
			items = append(items, next)
		}
		return token{kind: tokenList, list: items}, true

	case ')':
		p.offset++
		return token{kind: tokenClose}, true

	case '"':
		p.offset++
		var builder strings.Builder
		for p.offset < len(text) {
			character := text[p.offset]
			if '\\' == character && p.offset+1 < len(text) {
				p.offset++
				builder.WriteByte(text[p.offset])
				p.offset++
				continue
			}
			if '"' == character {
				p.offset++
				break
			}
			builder.WriteByte(character)
			p.offset++
		}
		return token{kind: tokenString, text: builder.String()}, true
	}

	start := p.offset
	for p.offset < len(text) {
		character := text[p.offset]
		if ' ' == character || '\t' == character || '(' == character || ')' == character {
			break
		}
		p.offset++
	}

	atom := text[start:p.offset]
	if "NIL" == strings.ToUpper(atom) {
		return token{kind: tokenNil}, true
	}
	return token{kind: tokenAtom, text: atom}, true
}

// rest returns the unparsed remainder of the line, trimmed.
func (p *responseParser) rest() string {
	var builder strings.Builder

	for ; p.index < len(p.segments); p.index++ {
		segment := p.segments[p.index]
		if segment.literal {
			builder.WriteString(segment.text)
			continue
		}

		offset := p.offset
		if offset > len(segment.text) {
			offset = len(segment.text)
		}
		builder.WriteString(segment.text[offset:])
		p.offset = 0
	}

	p.offset = 0
	return strings.TrimSpace(builder.String())
}

// all returns the remaining tokens of the line.
func (p *responseParser) all() []token {
	var result []token
	for {
		next, ok := p.next()
		if !ok {
			return result
		}
		result = append(result, next)
	}
}

// parseResponseLine is the line oriented entry point of the tokenizer.
func parseResponseLine(segments []lineSegment) response {
	parser := newResponseParser(segments)

	tag, ok := parser.next()
	if !ok {
		return response{}
	}

	if tokenAtom == tag.kind && "+" == tag.text {
		return response{tag: "+", kind: respContinuation, text: parser.rest()}
	}

	result := response{tag: tag.text}

	word, ok := parser.next()
	if !ok || tokenAtom != word.kind {
		// A line without a name is not something any command expects; report it
		// as data without a name so the caller reports the failure.
		result.kind = respData
		return result
	}

	upper := strings.ToUpper(word.text)
	if isStatusWord(upper) {
		result.kind = respStatus
		result.status = upper
		result.code, result.codeArgs, result.text = parseStatusCode(parser.rest())
		return result
	}

	result.kind = respData
	result.name = upper
	result.values = append(result.values, word)

	// args are the data arguments after the name (parseLine() semantics).
	for _, next := range parser.all() {
		result.values = append(result.values, next)
		if value, ok := next.textValue(); ok {
			result.args = append(result.args, value)
		}
	}

	return result
}

// parseStatusCode extracts the optional "[CODE args]" response code and the
// remaining human readable text of a status response.
func parseStatusCode(rest string) (code string, codeArgs []string, text string) {
	if strings.HasPrefix(rest, "[") {
		if end := strings.IndexByte(rest, ']'); 0 <= end {
			parser := parseText(rest[1:end])
			var values []token
			for _, item := range parser.all() {
				if "" == code {
					if value, ok := item.textValue(); ok {
						code = strings.ToUpper(value)
						continue
					}
				}
				values = append(values, item)
			}
			return code, tokenTexts(values), strings.TrimSpace(rest[end+1:])
		}
	}

	return "", nil, rest
}

// tokenTexts flattens tokens (including lists) into their string values.
func tokenTexts(values []token) []string {
	var result []string
	for _, value := range values {
		switch value.kind {
		case tokenAtom, tokenString:
			result = append(result, value.text)
		case tokenList:
			result = append(result, tokenTexts(value.list)...)
		}
	}
	return result
}
