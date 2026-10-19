package ojson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Parse decodes JSON text like JSON.parse: objects become *Obj with their
// key order kept, numbers float64.
func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if rest := bytes.TrimLeft(data[dec.InputOffset():], " \t\r\n"); len(rest) > 0 {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return v, nil
}

// MaxDepth is the deepest nesting of arrays and objects Parse accepts, so a
// hostile response can't exhaust the stack.
const MaxDepth = 1000

func parseValue(dec *json.Decoder, depth int) (any, error) {
	if depth > MaxDepth {
		return nil, errors.New("JSON is nested too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := New()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("invalid object key")
				}
				v, err := parseValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parseValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, errors.New("unexpected delimiter")
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return f, nil
	case string, bool, nil:
		return t, nil
	}
	return nil, errors.New("unexpected token")
}

// MustParse parses JSON text and panics on error (for literals in code and tests).
func MustParse(s string) any {
	v, err := Parse([]byte(s))
	if err != nil {
		panic(err)
	}
	return v
}

// Stringify encodes v like JSON.stringify(v) (no indentation). Undefined at
// the top level gives "undefined"'s JSON equivalent: ok is false.
func Stringify(v any) (string, bool) {
	var b strings.Builder
	if !write(&b, v, "", "") {
		return "", false
	}
	return b.String(), true
}

// StringifyIndent encodes v like JSON.stringify(v, null, 2), or "null" for Undefined.
func StringifyIndent(v any) string {
	var b strings.Builder
	if !write(&b, v, "  ", "") {
		return "null"
	}
	return b.String()
}

// write appends v's JSON; it returns false for values JSON.stringify skips (undefined).
func write(b *strings.Builder, v any, indent, prefix string) bool {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case undefinedType:
		return false
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		writeString(b, x)
	case *Obj:
		if x == nil {
			b.WriteString("null")
			return true
		}
		keys := x.Keys()
		first := true
		inner := prefix + indent
		for _, k := range keys {
			val := x.vals[k]
			if IsUndefined(val) {
				continue
			}
			if first {
				b.WriteByte('{')
				first = false
			} else {
				b.WriteByte(',')
			}
			if indent != "" {
				b.WriteByte('\n')
				b.WriteString(inner)
			}
			writeString(b, k)
			b.WriteByte(':')
			if indent != "" {
				b.WriteByte(' ')
			}
			write(b, val, indent, inner)
		}
		if first {
			b.WriteString("{}")
			return true
		}
		if indent != "" {
			b.WriteByte('\n')
			b.WriteString(prefix)
		}
		b.WriteByte('}')
	default:
		if f, ok := Num(v); ok {
			if math.IsNaN(f) || math.IsInf(f, 0) {
				b.WriteString("null")
			} else {
				b.WriteString(FormatNumber(f))
			}
			return true
		}
		if arr, ok := Arr(v); ok {
			if len(arr) == 0 {
				b.WriteString("[]")
				return true
			}
			inner := prefix + indent
			b.WriteByte('[')
			for i, e := range arr {
				if i > 0 {
					b.WriteByte(',')
				}
				if indent != "" {
					b.WriteByte('\n')
					b.WriteString(inner)
				}
				if !write(b, e, indent, inner) {
					b.WriteString("null")
				}
			}
			if indent != "" {
				b.WriteByte('\n')
				b.WriteString(prefix)
			}
			b.WriteByte(']')
			return true
		}
		// Any other Go value: go through encoding/json and back.
		if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
			b.WriteString("null")
			return true
		}
		raw, err := json.Marshal(v)
		if err != nil {
			b.WriteString("null")
			return true
		}
		parsed, err := Parse(raw)
		if err != nil {
			b.WriteString("null")
			return true
		}
		return write(b, parsed, indent, prefix)
	}
	return true
}

const hexDigits = "0123456789abcdef"

func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if c < 0x20 {
					b.WriteString(`\u00`)
					b.WriteByte(hexDigits[c>>4])
					b.WriteByte(hexDigits[c&0xf])
				} else {
					b.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString("�")
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

// MarshalJSON lets *Obj be used with encoding/json.
func (o *Obj) MarshalJSON() ([]byte, error) {
	s, ok := Stringify(o)
	if !ok {
		return []byte("null"), nil
	}
	return []byte(s), nil
}

// UnmarshalJSON decodes an object keeping its key order.
func (o *Obj) UnmarshalJSON(data []byte) error {
	v, err := Parse(data)
	if err != nil {
		return err
	}
	src, ok := v.(*Obj)
	if !ok {
		return errors.New("not a JSON object")
	}
	*o = *src
	return nil
}
