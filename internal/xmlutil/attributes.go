package xmlutil

// AttributeSpan locates an attribute in a start tag or processing instruction.
// Names and values retain their original spelling and XML escapes.
type AttributeSpan struct {
	NameStart, NameEnd   int
	ValueStart, ValueEnd int
}

// AttributeSpans reads quoted attributes without treating text inside a value
// as another attribute. It does not validate XML; callers use a token decoder
// when they need resolved names and values.
func AttributeSpans(tag string) []AttributeSpan {
	i := 1
	if len(tag) > i && tag[i] == '?' {
		i++
	}
	for i < len(tag) && !IsSpace(tag[i]) && tag[i] != '>' {
		i++
	}
	var spans []AttributeSpan
	for i < len(tag) {
		for i < len(tag) && IsSpace(tag[i]) {
			i++
		}
		if i == len(tag) || tag[i] == '/' || tag[i] == '>' || tag[i] == '?' {
			break
		}
		start := i
		for i < len(tag) && !IsSpace(tag[i]) && tag[i] != '=' && tag[i] != '>' {
			i++
		}
		end := i
		for i < len(tag) && IsSpace(tag[i]) {
			i++
		}
		if start == end || i == len(tag) || tag[i] != '=' {
			break
		}
		i++
		for i < len(tag) && IsSpace(tag[i]) {
			i++
		}
		if i == len(tag) || tag[i] != '\'' && tag[i] != '"' {
			break
		}
		quote := tag[i]
		i++
		valueStart := i
		for i < len(tag) && tag[i] != quote {
			i++
		}
		if i == len(tag) {
			break
		}
		spans = append(spans, AttributeSpan{start, end, valueStart, i})
		i++
	}
	return spans
}
