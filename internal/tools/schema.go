package tools

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

// Schema describes one argument. It produces the same JSON Schema the
// TypeScript server's zod definitions did, and validates (and strips unknown
// keys from) arguments the same way, with zod's error messages.
type Schema struct {
	kind     string // string, number, integer, boolean, array, object, record, any, union, literal
	desc     string
	optional bool
	nullable bool
	enum     []string
	minLen   *int
	maxLen   *int
	pattern  string
	re       *regexp.Regexp
	format   string
	min      *float64
	max      *float64
	exclMin  *float64
	items    *Schema
	minItems *int
	maxItems *int
	props    []Prop
	loose    bool
	values   *Schema
	anyOf    []*Schema
	constant any
}

// Prop is a named object property.
type Prop struct {
	Name   string
	Schema *Schema
}

// P builds a property.
func P(name string, s *Schema) Prop { return Prop{name, s} }

const maxSafe = 9007199254740991

func fptr(f float64) *float64 { return &f }
func iptr(i int) *int         { return &i }

func (s *Schema) clone() *Schema {
	c := *s
	return &c
}

// Str is a string.
func Str() *Schema { return &Schema{kind: "string"} }

// Enum is a string restricted to values.
func Enum(values ...string) *Schema { return &Schema{kind: "string", enum: values} }

// Number is a number.
func Number() *Schema { return &Schema{kind: "number"} }

// Int is an integer (a safe integer, as zod's .int()).
func Int() *Schema {
	return &Schema{kind: "integer", min: fptr(-maxSafe), max: fptr(maxSafe)}
}

// Bool is a boolean.
func Bool() *Schema { return &Schema{kind: "boolean"} }

// Any accepts any value.
func Any() *Schema { return &Schema{kind: "any"} }

// Array is an array of items.
func Array(items *Schema) *Schema { return &Schema{kind: "array", items: items} }

// Object is an object with these properties; unknown keys are dropped.
func Object(props ...Prop) *Schema { return &Schema{kind: "object", props: props} }

// LooseObject is an object whose unknown keys are passed through.
func LooseObject(props ...Prop) *Schema { return &Schema{kind: "object", props: props, loose: true} }

// Record is an object with arbitrary keys; values nil means any value.
func Record(values *Schema) *Schema { return &Schema{kind: "record", values: values} }

// Union accepts any of the options.
func Union(options ...*Schema) *Schema { return &Schema{kind: "union", anyOf: options} }

// Literal is exactly v.
func Literal(v any) *Schema { return &Schema{kind: "literal", constant: v} }

// D sets the description.
func (s *Schema) D(desc string) *Schema { c := s.clone(); c.desc = desc; return c }

// Opt makes the property optional.
func (s *Schema) Opt() *Schema { c := s.clone(); c.optional = true; return c }

// Null also accepts null.
func (s *Schema) Null() *Schema { c := s.clone(); c.nullable = true; return c }

// MinLen sets the minimum string length.
func (s *Schema) MinLen(n int) *Schema { c := s.clone(); c.minLen = iptr(n); return c }

// MaxLen sets the maximum string length.
func (s *Schema) MaxLen(n int) *Schema { c := s.clone(); c.maxLen = iptr(n); return c }

// Pattern requires the string to match a regular expression.
func (s *Schema) Pattern(p string) *Schema {
	c := s.clone()
	c.pattern = p
	c.re = regexp.MustCompile(p)
	return c
}

// URL requires a URL.
func (s *Schema) URL() *Schema { c := s.clone(); c.format = "uri"; return c }

// emailPattern is zod's email regex.
const emailPattern = `^(?:[A-Za-z0-9_'+\-]+\.)*[A-Za-z0-9_'+\-]*[A-Za-z0-9_+-]@(?:[A-Za-z0-9][A-Za-z0-9\-]*\.)+[A-Za-z]{2,}$`

// Email requires an email address.
func (s *Schema) Email() *Schema {
	c := s.Pattern(emailPattern)
	c.format = "email"
	return c
}

// Min sets an inclusive minimum (numbers) or minimum item count (arrays).
func (s *Schema) Min(n float64) *Schema {
	c := s.clone()
	if c.kind == "array" {
		c.minItems = iptr(int(n))
	} else {
		c.min, c.exclMin = fptr(n), nil
	}
	return c
}

// Max sets an inclusive maximum (numbers) or maximum item count (arrays).
func (s *Schema) Max(n float64) *Schema {
	c := s.clone()
	if c.kind == "array" {
		c.maxItems = iptr(int(n))
	} else {
		c.max = fptr(n)
	}
	return c
}

// Positive requires a number greater than zero.
func (s *Schema) Positive() *Schema { c := s.clone(); c.min, c.exclMin = nil, fptr(0); return c }

// ---------------------------------------------------------------- JSON Schema

// object returns the JSON Schema for a tool's input.
func inputSchema(props []Prop) *ojson.Obj {
	o := Object(props...).json()
	o.Set("$schema", "http://json-schema.org/draft-07/schema#")
	return o
}

func numVal(f float64) any { return f }

func (s *Schema) json() *ojson.Obj {
	if s.nullable {
		inner := s.clone()
		inner.nullable, inner.desc = false, ""
		ij := inner.json()
		out := ojson.New()
		if s.desc != "" {
			out.Set("description", s.desc)
		}
		if t, ok := ij.Get("type").(string); ok && ij.Len() == 1 {
			out.Set("type", []any{t, "null"})
		} else {
			out.Set("anyOf", []any{ij, ojson.New("type", "null")})
		}
		return out
	}
	o := ojson.New()
	if s.desc != "" {
		o.Set("description", s.desc)
	}
	switch s.kind {
	case "string":
		o.Set("type", "string")
		if s.format != "" {
			o.Set("format", s.format)
		}
		if s.enum != nil {
			o.Set("enum", ojson.Strings(s.enum))
		}
		if s.minLen != nil {
			o.Set("minLength", float64(*s.minLen))
		}
		if s.maxLen != nil {
			o.Set("maxLength", float64(*s.maxLen))
		}
		if s.pattern != "" {
			o.Set("pattern", s.pattern)
		}
	case "number", "integer":
		o.Set("type", s.kind)
		if s.exclMin != nil {
			o.Set("exclusiveMinimum", *s.exclMin)
		}
		if s.min != nil {
			o.Set("minimum", *s.min)
		}
		if s.max != nil {
			o.Set("maximum", *s.max)
		}
	case "boolean":
		o.Set("type", "boolean")
	case "array":
		if s.minItems != nil {
			o.Set("minItems", float64(*s.minItems))
		}
		if s.maxItems != nil {
			o.Set("maxItems", float64(*s.maxItems))
		}
		o.Set("type", "array")
		o.Set("items", s.items.json())
	case "object":
		o.Set("type", "object")
		props := ojson.New()
		var required []any
		for _, p := range s.props {
			props.Set(p.Name, p.Schema.json())
			if !p.Schema.optional {
				required = append(required, p.Name)
			}
		}
		o.Set("properties", props)
		if len(required) > 0 {
			o.Set("required", required)
		}
		if s.loose {
			o.Set("additionalProperties", ojson.New())
		}
	case "record":
		o.Set("type", "object")
		o.Set("propertyNames", ojson.New("type", "string"))
		if s.values != nil {
			o.Set("additionalProperties", s.values.json())
		} else {
			o.Set("additionalProperties", ojson.New())
		}
	case "union":
		opts := make([]any, len(s.anyOf))
		for i, x := range s.anyOf {
			opts[i] = x.json()
		}
		o.Set("anyOf", opts)
	case "literal":
		if _, ok := s.constant.(string); ok {
			o.Set("type", "string")
		} else {
			o.Set("type", "number")
		}
		o.Set("const", s.constant)
	}
	return o
}

// ---------------------------------------------------------------- validation

type issue struct {
	msg  string
	path []any
}

func (i issue) String() string {
	if len(i.path) == 0 {
		return i.msg
	}
	var b strings.Builder
	for n, p := range i.path {
		switch x := p.(type) {
		case int:
			fmt.Fprintf(&b, "[%d]", x)
		default:
			if n > 0 {
				b.WriteByte('.')
			}
			b.WriteString(fmt.Sprint(x))
		}
	}
	return i.msg + " at " + b.String()
}

func received(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case *ojson.Obj:
		return "object"
	}
	if ojson.IsUndefined(v) {
		return "undefined"
	}
	if ojson.IsNumber(v) {
		return "number"
	}
	if _, ok := ojson.Arr(v); ok {
		return "array"
	}
	return "unknown"
}

func pathWith(path []any, p any) []any {
	out := make([]any, len(path), len(path)+1)
	copy(out, path)
	return append(out, p)
}

func fmtNum(f float64) string { return ojson.FormatNumber(f) }

// parse validates v, appending problems to issues, and returns the value to use.
func (s *Schema) parse(v any, path []any, issues *[]issue) any {
	if s.nullable && v == nil {
		return nil
	}
	bad := func(msg string) any {
		*issues = append(*issues, issue{msg, path})
		return v
	}
	typeErr := func(expected string) any {
		return bad(fmt.Sprintf("Invalid input: expected %s, received %s", expected, received(v)))
	}
	switch s.kind {
	case "any":
		return v
	case "string":
		str, ok := v.(string)
		if !ok {
			if s.enum != nil {
				return bad("Invalid option: expected one of " + quoteList(s.enum))
			}
			return typeErr("string")
		}
		if s.enum != nil {
			for _, e := range s.enum {
				if e == str {
					return str
				}
			}
			return bad("Invalid option: expected one of " + quoteList(s.enum))
		}
		n, _ := ojson.Len(str)
		exact := s.minLen != nil && s.maxLen != nil && *s.minLen == *s.maxLen
		if s.minLen != nil && n < *s.minLen {
			if exact {
				bad(fmt.Sprintf("Too small: expected string to have exactly %d characters", *s.minLen))
			} else {
				bad(fmt.Sprintf("Too small: expected string to have >=%d characters", *s.minLen))
			}
		}
		if s.maxLen != nil && n > *s.maxLen {
			if exact {
				bad(fmt.Sprintf("Too big: expected string to have exactly %d characters", *s.maxLen))
			} else {
				bad(fmt.Sprintf("Too big: expected string to have <=%d characters", *s.maxLen))
			}
		}
		switch s.format {
		case "uri":
			if !validURL(str) {
				bad("Invalid URL")
			}
		case "email":
			if !s.re.MatchString(str) {
				bad("Invalid email address")
			}
		default:
			if s.re != nil && !s.re.MatchString(str) {
				bad("Invalid string: must match pattern /" + s.pattern + "/")
			}
		}
		return str
	case "number", "integer":
		f, ok := ojson.Num(v)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return typeErr("number")
		}
		if s.kind == "integer" && f != math.Trunc(f) {
			return typeErr("int")
		}
		if s.exclMin != nil && !(f > *s.exclMin) {
			bad("Too small: expected number to be >" + fmtNum(*s.exclMin))
		}
		if s.min != nil && f < *s.min {
			bad("Too small: expected number to be >=" + fmtNum(*s.min))
		}
		if s.max != nil && f > *s.max {
			bad("Too big: expected number to be <=" + fmtNum(*s.max))
		}
		return f
	case "boolean":
		if _, ok := v.(bool); !ok {
			return typeErr("boolean")
		}
		return v
	case "literal":
		if !ojson.StrictEq(v, s.constant) {
			return bad("Invalid input: expected " + ojson.String(mustStringify(s.constant)))
		}
		return v
	case "union":
		for _, opt := range s.anyOf {
			var sub []issue
			out := opt.parse(v, path, &sub)
			if len(sub) == 0 {
				return out
			}
		}
		return bad("Invalid input")
	case "array":
		arr, ok := ojson.Arr(v)
		if !ok {
			return typeErr("array")
		}
		if s.minItems != nil && len(arr) < *s.minItems {
			bad(fmt.Sprintf("Too small: expected array to have >=%d items", *s.minItems))
		}
		if s.maxItems != nil && len(arr) > *s.maxItems {
			bad(fmt.Sprintf("Too big: expected array to have <=%d items", *s.maxItems))
		}
		out := make([]any, len(arr))
		for i, e := range arr {
			out[i] = s.items.parse(e, pathWith(path, i), issues)
		}
		return out
	case "object":
		o, ok := ojson.AsObj(v)
		if !ok {
			return typeErr("object")
		}
		out := ojson.New()
		known := map[string]bool{}
		for _, p := range s.props {
			known[p.Name] = true
			val := o.Get(p.Name)
			if ojson.IsUndefined(val) {
				if p.Schema.optional {
					continue
				}
			}
			out.Set(p.Name, p.Schema.parse(val, pathWith(path, p.Name), issues))
		}
		if s.loose {
			for _, k := range o.Keys() {
				if !known[k] {
					out.Set(k, o.Get(k))
				}
			}
		}
		return out
	case "record":
		o, ok := ojson.AsObj(v)
		if !ok {
			return typeErr("record")
		}
		out := ojson.New()
		for _, k := range o.Keys() {
			val := o.Get(k)
			if s.values != nil {
				val = s.values.parse(val, pathWith(path, k), issues)
			}
			out.Set(k, val)
		}
		return out
	}
	return v
}

func mustStringify(v any) any {
	s, _ := ojson.Stringify(v)
	return s
}

func quoteList(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Quote(v)
	}
	return strings.Join(parts, "|")
}

// validURL approximates zod's z.string().url(): something `new URL()` accepts.
func validURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*$`).MatchString(u.Scheme) {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss", "ftp", "file":
		return u.Host != "" || strings.ToLower(u.Scheme) == "file"
	}
	return true
}
