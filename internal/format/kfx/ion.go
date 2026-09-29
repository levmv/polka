package kfx

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/levmv/polka/internal/xmlutil"
)

// KFX stores structured fragments as Ion 1.0. Decode only the binary wire
// representation; interpretation of book fields belongs to the content reader.
// https://amazon-ion.github.io/ion-docs/docs/binary.html
const (
	ionPadding byte = iota
	ionBool
	ionPositiveInt
	ionNegativeInt
	ionFloat
	ionDecimal
	ionTimestamp
	ionSymbol
	ionString
	ionClob
	ionBlob
	ionList
	ionSexp
	ionStruct
	ionAnnotation
	ionNull // A decoded typed null; type 15 itself is reserved on the wire.
)

type value struct {
	kind    byte
	text    string
	number  float64
	integer int64
	list    []value
	fields  []field
}

type field struct {
	name  string
	value value
}

func (v value) get(id int) value { return v.named("$" + strconv.Itoa(id)) }
func (v value) named(name string) value {
	for _, f := range v.fields {
		if f.name == name {
			return f.value
		}
	}
	return value{}
}
func (v value) id() string {
	if v.kind == ionPositiveInt || v.kind == ionNegativeInt {
		return strconv.FormatInt(v.integer, 10)
	}
	return v.text
}
func (v value) numeric() float64 {
	if v.kind == ionPositiveInt || v.kind == ionNegativeInt {
		return float64(v.integer)
	}
	return v.number
}

type symbols map[uint64]string

func (s symbols) name(id uint64) string {
	if name, ok := s[id]; ok && name != "" {
		return name
	}
	switch id {
	case 4:
		return "name"
	case 5:
		return "version"
	case 6:
		return "imports"
	case 7:
		return "symbols"
	case 8:
		return "max_id"
	}
	return "$" + strconv.FormatUint(id, 10)
}

type ionDecoder struct {
	ctx              context.Context
	symbols          symbols
	remaining        int
	replacedControls bool
}

func (d *ionDecoder) decode(data []byte) (value, error) {
	var result value
	found := false
	for len(data) > 0 {
		if bytes.HasPrefix(data, []byte{0xe0, 1, 0, 0xea}) {
			data = data[4:]
			continue
		}
		v, rest, err := d.read(data, 0)
		if err != nil {
			return value{}, err
		}
		data = rest
		if v.kind == ionPadding {
			continue
		}
		if found {
			return value{}, invalid("multiple Ion values in a fragment")
		}
		result, found = v, true
	}
	return result, nil
}

func (d *ionDecoder) read(data []byte, depth int) (value, []byte, error) {
	if err := d.ctx.Err(); err != nil {
		return value{}, nil, err
	}
	d.remaining--
	if depth > 64 || d.remaining < 0 {
		return value{}, nil, fmt.Errorf("%w: Ion structure is too large or deeply nested", ErrLimit)
	}
	if len(data) == 0 {
		return value{}, nil, invalid("truncated Ion value")
	}
	typ, length := data[0]>>4, uint64(data[0]&15)
	data = data[1:]
	if typ == ionNull {
		return value{}, nil, invalid("reserved Ion type")
	}
	if length == 15 {
		if typ == ionAnnotation {
			return value{}, nil, invalid("null Ion annotation")
		}
		return value{kind: ionNull}, data, nil
	}
	v := value{kind: typ}
	if typ == ionBool {
		if length > 1 {
			return value{}, nil, invalid("invalid Ion boolean")
		}
		v.integer = int64(length)
		return v, data, nil
	}
	if length == 14 || typ == ionStruct && length == 1 {
		var err error
		length, data, err = readVarUInt(data)
		if err != nil {
			return value{}, nil, err
		}
	}
	if length > uint64(len(data)) {
		return value{}, nil, invalid("truncated Ion payload")
	}
	body, rest := data[:length], data[length:]
	switch typ {
	case ionPadding: // Padding is not a value.
	case ionPositiveInt, ionNegativeInt, ionSymbol:
		n, err := readUInt(body)
		if err != nil {
			return value{}, nil, err
		}
		if typ == ionSymbol {
			v.text = d.symbols.name(n)
			break
		}
		if n > math.MaxInt64 || typ == ionNegativeInt && n == 0 {
			return value{}, nil, invalid("Ion integer is out of range")
		}
		v.integer = int64(n)
		if typ == ionNegativeInt {
			v.integer = -v.integer
		}
	case ionFloat:
		switch len(body) {
		case 0:
		case 4:
			v.number = float64(math.Float32frombits(binary.BigEndian.Uint32(body)))
		case 8:
			v.number = math.Float64frombits(binary.BigEndian.Uint64(body))
		default:
			return value{}, nil, invalid("invalid Ion float")
		}
	case ionDecimal:
		if len(body) == 0 {
			break
		}
		if len(body) > 18 {
			return value{}, nil, invalid("Ion decimal is too large")
		}
		negative := body[0]&0x40 != 0
		exponentBytes := append([]byte(nil), body...)
		exponentBytes[0] &= ^byte(0x40)
		exponent, coefficient, err := readVarUInt(exponentBytes)
		if err != nil || exponent > 308 {
			return value{}, nil, invalid("invalid Ion decimal exponent")
		}
		power := int(exponent)
		if negative {
			power = -power
		}
		sign := 1.0
		if len(coefficient) > 0 && coefficient[0]&0x80 != 0 {
			sign = -1
			coefficient[0] &= 0x7f
		}
		n, err := readUInt(coefficient)
		if err != nil {
			return value{}, nil, err
		}
		v.number = sign * float64(n) * math.Pow10(power)
	case ionTimestamp, ionClob, ionBlob:
		// Timestamps and binary values occur in auxiliary metadata. Keep their
		// bounded payload opaque; book resources are separate container entities.
		v.text = string(body)
	case ionString:
		if !utf8.Valid(body) {
			return value{}, nil, invalid("invalid UTF-8 in Ion string")
		}
		v.text = strings.Map(func(r rune) rune {
			if xmlutil.ValidXML10Char(r) {
				return r
			}
			d.replacedControls = true
			// Replacement preserves code point offsets used by styles and links.
			return '\ufffd'
		}, string(body))
	case ionList, ionSexp, ionStruct:
		for len(body) > 0 {
			var key uint64
			var err error
			if typ == ionStruct {
				key, body, err = readVarUInt(body)
				if err != nil {
					return value{}, nil, err
				}
			}
			var child value
			child, body, err = d.read(body, depth+1)
			if err != nil {
				return value{}, nil, err
			}
			if child.kind == ionPadding {
				continue
			}
			if typ == ionStruct {
				v.fields = append(v.fields, field{d.symbols.name(key), child})
			} else {
				v.list = append(v.list, child)
			}
		}
	case ionAnnotation:
		n, payload, err := readVarUInt(body)
		if err != nil || n == 0 || n >= uint64(len(payload)) {
			return value{}, nil, invalid("invalid Ion annotation")
		}
		annotations := payload[:n]
		for len(annotations) > 0 {
			_, annotations, err = readVarUInt(annotations)
			if err != nil {
				return value{}, nil, err
			}
		}
		var trailing []byte
		v, trailing, err = d.read(payload[n:], depth+1)
		if err != nil {
			return value{}, nil, err
		}
		if len(trailing) != 0 || v.kind == ionPadding {
			return value{}, nil, invalid("invalid annotated Ion value")
		}
	default:
		return value{}, nil, invalid("unknown Ion type")
	}
	return v, rest, nil
}

func readVarUInt(data []byte) (uint64, []byte, error) {
	var n uint64
	for i, b := range data {
		if n > math.MaxUint64>>7 {
			break
		}
		n = n<<7 | uint64(b&127)
		if b&128 != 0 {
			return n, data[i+1:], nil
		}
	}
	return 0, nil, invalid("invalid Ion variable integer")
}

func readUInt(data []byte) (uint64, error) {
	if len(data) > 8 {
		return 0, invalid("Ion integer is too large")
	}
	var n uint64
	for _, b := range data {
		n = n<<8 | uint64(b)
	}
	return n, nil
}
