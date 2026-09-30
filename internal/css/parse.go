package css

import "strings"

// Component groups a function or bracketed block without allocating its
// children. Closed distinguishes CSS's implicit EOF closure from an actual
// closing delimiter; preservation and validation can choose different policies.
type Component struct {
	Token
	Children Values
}

type Components struct {
	lex lexer
	Component
	err error
}

func (v Values) Components() Components { return Components{lex: v.lexer()} }
func (p *Components) Err() error        { return p.err }

func (p *Components) Next() bool {
	if p.err != nil {
		return false
	}
	t := p.lex.next()
	if t.Kind == EOF {
		return false
	}
	return p.consume(t)
}

func (p *Components) consume(t Token) bool {
	p.Component = Component{Token: t}
	if t.Kind != Function && t.Kind != Block {
		return true
	}
	var stack [maxDepth]byte
	stack[0] = closing(t)
	depth, start, end := 1, t.End, t.End
	for depth > 0 {
		if depth+int(p.lex.depth) > maxDepth {
			p.err = ErrLimit
			return false
		}
		next := p.lex.next()
		end = next.Start
		if next.Kind == EOF {
			p.Closed = false
			break
		}
		if next.Kind == Function || next.Kind == Block {
			if depth == maxDepth {
				p.err = ErrLimit
				return false
			}
			stack[depth] = closing(next)
			depth++
		} else if next.Is(stack[depth-1]) {
			depth--
		}
	}
	p.End = p.lex.pos
	p.Children = p.lex.slice(start, end)
	p.Children.depth++
	p.Children.inValue = p.lex.inValue || t.Kind == Function || t.Delim != '{'
	return true
}

func closing(t Token) byte {
	switch t.Delim {
	case '[':
		return ']'
	case '{':
		return '}'
	default:
		return ')'
	}
}

// Significant skips comments and whitespace. Selectors which need descendant
// combinators should use Next and keep whitespace (comments are not whitespace).
func (p *Components) Significant() bool {
	for p.Next() {
		if !p.Trivia() {
			return true
		}
	}
	return false
}

func (v Values) Single() (Component, bool) {
	p := v.Components()
	if !p.Significant() {
		return Component{}, false
	}
	c := p.Component
	return c, !p.Significant() && p.Err() == nil
}

func (v Values) Keyword(name string) bool {
	c, ok := v.Single()
	return ok && c.Kind == Ident && equalKeyword(c.Text(), name)
}

func (v Values) Empty() bool {
	p := v.Components()
	return !p.Significant() && p.Err() == nil
}

// Walk visits component values in source order, including nested values. URLs
// in quoted strings and comments are never interpreted as CSS syntax.
func (v Values) Walk(visit func(Component) error) error {
	p := v.Components()
	for p.Next() {
		if err := visit(p.Component); err != nil {
			return err
		}
		if p.Kind == Function || p.Kind == Block {
			if err := p.Children.Walk(visit); err != nil {
				return err
			}
		}
	}
	return p.Err()
}

type Rule struct {
	Span
	Name     string // decoded at-keyword, empty for a qualified rule
	Prelude  Values
	Body     Values
	HasBlock bool
}

type Rules struct {
	p Components
	Rule
}

func (v Values) Rules() Rules { return Rules{p: v.Components()} }
func (p *Rules) Err() error   { return p.p.Err() }
func (p *Rules) Next() bool {
	for p.p.Significant() {
		first := p.p.Component
		if first.Kind == CDO || first.Kind == CDC || first.Is(';') || first.Is('}') {
			continue
		}
		p.Rule = Rule{Span: first.Span}
		start := first.Start
		if first.Kind == AtKeyword {
			p.Name = first.Text()
			start = first.End
			if !p.p.Next() {
				p.Prelude = first.slice(start, p.p.lex.pos)
				p.End = p.p.lex.pos
				return p.Err() == nil
			}
		}
		for {
			c := p.p.Component
			if c.Kind == Block && c.Delim == '{' {
				p.Prelude, p.Body = first.slice(start, c.Start), c.Children
				p.End, p.HasBlock = c.End, true
				return true
			}
			if p.Name != "" && c.Is(';') {
				p.Prelude, p.End = first.slice(start, c.Start), c.End
				return true
			}
			if !p.p.Next() {
				p.Prelude, p.End = first.slice(start, p.p.lex.pos), p.p.lex.pos
				return p.Name != "" && p.Err() == nil
			}
		}
	}
	return false
}

type Declaration struct {
	Span             // Includes the trailing semicolon, when present.
	Name      string // Decoded; custom property names retain their case.
	Value     Values // Source value without the !important suffix.
	Important bool
}

type Declarations struct {
	p Components
	Declaration
}

// Declarations retains order and duplicates, skipping malformed statements at
// their next boundary. It does not validate a property's grammar. Unknown
// properties and functions remain available, and Raw retains discarded syntax.
func (v Values) Declarations() Declarations { return Declarations{p: v.Components()} }
func (p *Declarations) Err() error          { return p.p.Err() }
func (p *Declarations) Next() bool {
	for p.p.Significant() {
		first := p.p.Component
		if first.Is(';') {
			continue
		}
		valid := first.Kind == Ident && p.p.Significant() && p.p.Is(':')
		p.Declaration = Declaration{Span: first.Span, Name: first.Text()}
		custom := strings.HasPrefix(p.Name, "--")
		if !custom {
			p.Name = lowerKeyword(p.Name)
		}
		start, end := p.p.End, p.p.End
		blockEnd := 0
		var previous, last Component
		// Invalid statements and nested rules are skipped as whole components,
		// so a colon or semicolon inside them cannot become a declaration.
		if !valid && (p.p.Is(';') || p.p.Kind == Block && p.p.Delim == '{') {
			continue
		}
		for p.p.Next() {
			c := p.p.Component
			if c.Is(';') {
				break
			}
			if c.Kind == Block && c.Delim == '{' {
				// An ordinary property can only use {} as its whole value.
				// Otherwise this is a nested rule, e.g. a:hover {...}.
				if !valid || !custom && last.Kind != EOF {
					valid = false
					break
				}
				if !custom {
					blockEnd = c.End
				}
			}
			if c.Kind == BadString || c.Kind == BadURL || c.Is(')') || c.Is(']') || c.Is('}') {
				valid = false
			}
			if !c.Trivia() {
				if last.Kind == EOF {
					start = c.Start
				}
				previous, last = last, c
				end = c.End
			}
		}
		if p.Err() != nil {
			return false
		}
		if previous.Is('!') && last.Kind == Ident && equalKeyword(last.Text(), "important") {
			p.Important, end = true, previous.Start
		}
		if blockEnd != 0 && (!valid || !first.slice(blockEnd, end).Empty()) {
			// Resume after the nested rule's block, not after the following
			// declaration that made this look like an invalid property.
			p.p.lex.pos = blockEnd
			continue
		}
		if !valid {
			continue
		}
		p.End = p.p.lex.pos
		p.Value = first.slice(start, end)
		p.Value.inValue = true
		return true
	}
	return false
}

// List splits only at top-level separators, retaining each item's source bytes.
// It works for selector lists, font sources and comma-separated function args.
type List struct {
	p         Components
	separator byte
	done      bool
	Values
}

func (v Values) Split(separator byte) List {
	return List{p: v.Components(), separator: separator}
}
func (p *List) Err() error { return p.p.Err() }
func (p *List) Next() bool {
	if p.done {
		return false
	}
	start := p.p.lex.pos
	for p.p.Next() {
		if p.p.Is(p.separator) {
			p.Values = p.p.lex.slice(start, p.p.Start)
			return true
		}
	}
	p.done = true
	p.Values = p.p.lex.slice(start, p.p.lex.pos)
	return p.Err() == nil
}
