package profile

import (
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxUNICNestingDepth = 64

type fieldSchema struct {
	fields         map[string]fieldRule
	allowUnknown   bool
	mapValueSchema *fieldSchema
	attributeCount int
	attributes     map[int]struct{}
}

type fieldRule struct {
	child    *fieldSchema
	repeated bool
}

func schemaForType(typ reflect.Type) (*fieldSchema, error) {
	return schemaForStruct(typ, make(map[reflect.Type]bool))
}

func schemaForStruct(typ reflect.Type, active map[reflect.Type]bool) (*fieldSchema, error) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("profile schema type %s is not a struct", typ)
	}
	if active[typ] {
		return nil, fmt.Errorf("recursive UNIC schema type %s is unsupported", typ)
	}
	active[typ] = true
	defer delete(active, typ)

	schema := &fieldSchema{
		fields:     make(map[string]fieldRule, typ.NumField()),
		attributes: make(map[int]struct{}),
	}
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		tag := strings.TrimSpace(field.Tag.Get("unic"))
		if tag == "" || tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("profile schema field %s has an empty UNIC name", field.Name)
		}
		attributeIndex := 0
		for _, option := range strings.Split(options, ",") {
			option = strings.TrimSpace(option)
			if !strings.HasPrefix(option, "attr=") {
				continue
			}
			attribute, err := strconv.Atoi(strings.TrimPrefix(option, "attr="))
			if err != nil || attribute <= 0 {
				return nil, fmt.Errorf("profile schema field %s has invalid attr tag", field.Name)
			}
			if attribute > schema.attributeCount {
				schema.attributeCount = attribute
			}
			if _, exists := schema.attributes[attribute]; exists {
				return nil, fmt.Errorf("profile schema has duplicate attr=%d", attribute)
			}
			schema.attributes[attribute] = struct{}{}
			attributeIndex = attribute
		}
		if attributeIndex > 0 {
			continue
		}
		if _, exists := schema.fields[name]; exists {
			return nil, fmt.Errorf("profile schema has duplicate UNIC field %q", name)
		}

		childType := field.Type
		repeated := false
		if childType.Kind() == reflect.Slice || childType.Kind() == reflect.Array {
			repeated = true
			childType = childType.Elem()
		}
		child, err := schemaForValueType(childType, active)
		if err != nil {
			return nil, fmt.Errorf("profile schema field %s: %w", name, err)
		}
		schema.fields[name] = fieldRule{child: child, repeated: repeated}
	}
	return schema, nil
}

func schemaForValueType(typ reflect.Type, active map[reflect.Type]bool) (*fieldSchema, error) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		return schemaForStruct(typ, active)
	case reflect.Map:
		value, err := schemaForValueType(typ.Elem(), active)
		if err != nil {
			return nil, err
		}
		return &fieldSchema{allowUnknown: true, mapValueSchema: value}, nil
	case reflect.Interface:
		return &fieldSchema{allowUnknown: true}, nil
	default:
		return nil, nil
	}
}

func rejectUnknownFields(data []byte, typ reflect.Type, documentName string) error {
	schema, err := schemaForType(typ)
	if err != nil {
		return fmt.Errorf("%s schema: %w", documentName, err)
	}
	parser, err := newStrictUNICParser(data)
	if err != nil {
		return fmt.Errorf("%s: %w", documentName, err)
	}
	if err := parser.parseFields(schema, documentName, true, 0); err != nil {
		return fmt.Errorf("%s: %w", documentName, err)
	}
	return nil
}

type strictTokenKind uint8

const (
	strictEOF strictTokenKind = iota
	strictIdent
	strictString
	strictLBrace
	strictRBrace
	strictLBracket
	strictRBracket
	strictLParen
	strictRParen
	strictSemicolon
	strictComma
)

type strictToken struct {
	kind   strictTokenKind
	value  string
	line   int
	column int
}

type strictUNICParser struct {
	source []rune
	index  int
	line   int
	column int
	peeked *strictToken
}

func newStrictUNICParser(data []byte) (*strictUNICParser, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	return &strictUNICParser{source: []rune(string(data)), line: 1, column: 1}, nil
}

func (p *strictUNICParser) parseFields(schema *fieldSchema, path string, top bool, depth int) error {
	if depth > maxUNICNestingDepth {
		return p.errorAt(p.line, p.column, "UNIC nesting exceeds %d levels", maxUNICNestingDepth)
	}
	if schema == nil {
		schema = &fieldSchema{fields: map[string]fieldRule{}}
	}
	occurrences := make(map[string]int)
	for {
		token, err := p.peek()
		if err != nil {
			return err
		}
		switch token.kind {
		case strictEOF:
			if top {
				return nil
			}
			return p.errorAt(token.line, token.column, "unclosed UNIC block")
		case strictRBrace:
			if top {
				return p.errorAt(token.line, token.column, "unexpected '}'")
			}
			_, _ = p.next()
			return nil
		case strictIdent, strictString:
			if err := p.parseField(schema, path, depth, occurrences); err != nil {
				return err
			}
		default:
			return p.errorAt(token.line, token.column, "expected a field name")
		}
	}
}

func (p *strictUNICParser) parseField(schema *fieldSchema, parentPath string, depth int, occurrences map[string]int) error {
	key, err := p.next()
	if err != nil {
		return err
	}
	rule, known := schema.fields[key.value]
	if !known && !schema.allowUnknown {
		fieldPath := joinField(parentPath, key.value)
		return p.errorAt(key.line, key.column, "unknown field %q at %s; remove it or correct the field name", key.value, fieldPath)
	}
	if schema.allowUnknown {
		rule.child = schema.mapValueSchema
	}
	fieldPath := joinField(parentPath, key.value)
	if occurrences[key.value] > 0 && !rule.repeated {
		return p.errorAt(key.line, key.column, "duplicate field %q at %s; keep only one value", key.value, fieldPath)
	}
	if rule.repeated {
		fieldPath += fmt.Sprintf("[%d]", occurrences[key.value])
	}
	occurrences[key.value]++
	return p.parseFieldValue(rule.child, fieldPath, depth+1)
}

func (p *strictUNICParser) parseFieldValue(child *fieldSchema, path string, depth int) error {
	token, err := p.peek()
	if err != nil {
		return err
	}
	switch token.kind {
	case strictLBrace:
		return p.parseBlock(child, path, depth)
	case strictLBracket:
		if err := p.parseList(child, path, depth); err != nil {
			return err
		}
		return p.expect(strictSemicolon, "expected ';' after list")
	case strictLParen:
		if err := p.parseMap(child, path, depth); err != nil {
			return err
		}
		return p.expect(strictSemicolon, "expected ';' after map")
	case strictSemicolon:
		_, _ = p.next()
		return nil
	case strictEOF, strictRBrace:
		return p.errorAt(token.line, token.column, "expected value or ';' after %s", path)
	default:
		attributes := 0
		for {
			token, err = p.next()
			if err != nil {
				return err
			}
			switch token.kind {
			case strictIdent, strictString:
				attributes++
			case strictSemicolon:
				return nil
			case strictLBrace:
				if attributes > 0 {
					if child == nil || child.attributeCount == 0 {
						return p.errorAt(token.line, token.column, "block attributes are not supported for %s", path)
					}
					if attributes > child.attributeCount {
						return p.errorAt(token.line, token.column, "too many attributes for %s", path)
					}
				}
				return p.parseBlockBody(child, path, depth+1, token)
			default:
				return p.errorAt(token.line, token.column, "unexpected token in value for %s", path)
			}
		}
	}
}

func (p *strictUNICParser) parseBlock(schema *fieldSchema, path string, depth int) error {
	open, err := p.next()
	if err != nil {
		return err
	}
	return p.parseBlockBody(schema, path, depth, open)
}

func (p *strictUNICParser) parseBlockBody(schema *fieldSchema, path string, depth int, open strictToken) error {
	if depth > maxUNICNestingDepth {
		return p.errorAt(open.line, open.column, "UNIC nesting exceeds %d levels", maxUNICNestingDepth)
	}
	if open.kind != strictLBrace {
		return p.errorAt(open.line, open.column, "expected '{'")
	}
	return p.parseFields(schema, path, false, depth)
}

func (p *strictUNICParser) parseList(elementSchema *fieldSchema, path string, depth int) error {
	_, _ = p.next()
	index := 0
	for {
		token, err := p.peek()
		if err != nil {
			return err
		}
		if token.kind == strictRBracket {
			_, _ = p.next()
			return nil
		}
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if err := p.parseValue(elementSchema, itemPath, depth+1); err != nil {
			return err
		}
		index++
		token, err = p.peek()
		if err != nil {
			return err
		}
		switch token.kind {
		case strictComma:
			_, _ = p.next()
		case strictRBracket:
		default:
			return p.errorAt(token.line, token.column, "expected ',' or ']' in list")
		}
	}
}

func (p *strictUNICParser) parseMap(valueSchema *fieldSchema, path string, depth int) error {
	_, _ = p.next()
	index := 0
	for {
		token, err := p.peek()
		if err != nil {
			return err
		}
		if token.kind == strictRParen {
			_, _ = p.next()
			if index%2 != 0 {
				return p.errorAt(token.line, token.column, "map must contain key/value pairs")
			}
			return nil
		}
		elementSchema := (*fieldSchema)(nil)
		if index%2 == 1 {
			elementSchema = valueSchema
		}
		if err := p.parseValue(elementSchema, fmt.Sprintf("%s.map[%d]", path, index/2), depth+1); err != nil {
			return err
		}
		index++
		token, err = p.peek()
		if err != nil {
			return err
		}
		switch token.kind {
		case strictComma:
			_, _ = p.next()
		case strictRParen:
		default:
			return p.errorAt(token.line, token.column, "expected ',' or ')' in map")
		}
	}
}

func (p *strictUNICParser) parseValue(schema *fieldSchema, path string, depth int) error {
	if depth > maxUNICNestingDepth {
		return p.errorAt(p.line, p.column, "UNIC nesting exceeds %d levels", maxUNICNestingDepth)
	}
	token, err := p.peek()
	if err != nil {
		return err
	}
	switch token.kind {
	case strictLBrace:
		return p.parseBlock(schema, path, depth+1)
	case strictLBracket:
		return p.parseList(schema, path, depth+1)
	case strictLParen:
		return p.parseMap(schema, path, depth+1)
	case strictIdent, strictString:
		_, _ = p.next()
		return nil
	default:
		return p.errorAt(token.line, token.column, "expected a value")
	}
}

func (p *strictUNICParser) expect(kind strictTokenKind, message string) error {
	token, err := p.next()
	if err != nil {
		return err
	}
	if token.kind != kind {
		return p.errorAt(token.line, token.column, message)
	}
	return nil
}

func (p *strictUNICParser) peek() (strictToken, error) {
	if p.peeked != nil {
		return *p.peeked, nil
	}
	token, err := p.scan()
	if err != nil {
		return strictToken{}, err
	}
	p.peeked = &token
	return token, nil
}

func (p *strictUNICParser) next() (strictToken, error) {
	if p.peeked != nil {
		token := *p.peeked
		p.peeked = nil
		return token, nil
	}
	return p.scan()
}

func (p *strictUNICParser) scan() (strictToken, error) {
	for p.index < len(p.source) {
		r := p.source[p.index]
		if unicode.IsSpace(r) {
			p.advance()
			continue
		}
		if r == '#' {
			for p.index < len(p.source) && p.advance() != '\n' {
			}
			continue
		}
		break
	}
	if p.index >= len(p.source) {
		return strictToken{kind: strictEOF, line: p.line, column: p.column}, nil
	}

	line, column := p.line, p.column
	r := p.source[p.index]
	switch r {
	case '{':
		p.advance()
		return strictToken{kind: strictLBrace, value: "{", line: line, column: column}, nil
	case '}':
		p.advance()
		return strictToken{kind: strictRBrace, value: "}", line: line, column: column}, nil
	case '[':
		p.advance()
		return strictToken{kind: strictLBracket, value: "[", line: line, column: column}, nil
	case ']':
		p.advance()
		return strictToken{kind: strictRBracket, value: "]", line: line, column: column}, nil
	case '(':
		p.advance()
		return strictToken{kind: strictLParen, value: "(", line: line, column: column}, nil
	case ')':
		p.advance()
		return strictToken{kind: strictRParen, value: ")", line: line, column: column}, nil
	case ';':
		p.advance()
		return strictToken{kind: strictSemicolon, value: ";", line: line, column: column}, nil
	case ',':
		p.advance()
		return strictToken{kind: strictComma, value: ",", line: line, column: column}, nil
	case '\'', '"':
		return p.scanQuoted(r)
	case '`':
		return p.scanRaw()
	default:
		return p.scanIdent()
	}
}

func (p *strictUNICParser) scanQuoted(quote rune) (strictToken, error) {
	line, column := p.line, p.column
	p.advance()
	var value strings.Builder
	for p.index < len(p.source) {
		r := p.advance()
		if r == quote {
			return strictToken{kind: strictString, value: value.String(), line: line, column: column}, nil
		}
		value.WriteRune(r)
	}
	return strictToken{}, p.errorAt(line, column, "unterminated string")
}

func (p *strictUNICParser) scanRaw() (strictToken, error) {
	line, column := p.line, p.column
	for range 3 {
		if p.index >= len(p.source) || p.advance() != '`' {
			return strictToken{}, p.errorAt(line, column, "raw strings must start with triple backticks")
		}
	}
	var value strings.Builder
	ticks := 0
	for p.index < len(p.source) {
		r := p.advance()
		if r == '`' {
			ticks++
			if ticks == 3 {
				return strictToken{kind: strictString, value: value.String(), line: line, column: column}, nil
			}
			continue
		}
		for ticks > 0 {
			value.WriteRune('`')
			ticks--
		}
		value.WriteRune(r)
	}
	return strictToken{}, p.errorAt(line, column, "unterminated raw string")
}

func (p *strictUNICParser) scanIdent() (strictToken, error) {
	line, column := p.line, p.column
	start := p.index
	for p.index < len(p.source) && !unicode.IsSpace(p.source[p.index]) && !isUNICDelimiter(p.source[p.index]) {
		p.advance()
	}
	if start == p.index {
		return strictToken{}, p.errorAt(line, column, "unexpected character %q", p.source[p.index])
	}
	return strictToken{kind: strictIdent, value: string(p.source[start:p.index]), line: line, column: column}, nil
}

func (p *strictUNICParser) advance() rune {
	r := p.source[p.index]
	p.index++
	if r == '\n' {
		p.line++
		p.column = 1
	} else {
		p.column++
	}
	return r
}

func (p *strictUNICParser) errorAt(line, column int, format string, args ...any) error {
	return fmt.Errorf("unic:%d:%d: %s", line, column, fmt.Sprintf(format, args...))
}

func isUNICDelimiter(r rune) bool {
	switch r {
	case '{', '}', '[', ']', '(', ')', '#', ';', ',', '\'', '"', '`':
		return true
	default:
		return false
	}
}

func joinField(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}
