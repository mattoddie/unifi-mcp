// Package ojson models JSON values the way JavaScript does, so the Go server
// produces exactly what the original TypeScript implementation did: objects
// keep their key order, a missing value ("undefined") is distinct from null,
// numbers are float64 and print like JavaScript numbers, and the helpers
// implement JavaScript's truthiness, ?? and === rules.
//
// Values are nil (null), Undefined, bool, float64 (or any Go integer type),
// string, []any and *Obj.
package ojson

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

type undefinedType struct{}

// Undefined is JavaScript's undefined: a missing value. Setting a key to
// Undefined removes it, and it is never written out as an object member.
var Undefined any = undefinedType{}

// IsUndefined reports whether v is Undefined.
func IsUndefined(v any) bool {
	_, ok := v.(undefinedType)
	return ok
}

// IsNullish reports whether v is null or undefined (what ?? skips).
func IsNullish(v any) bool {
	return v == nil || IsUndefined(v)
}

// Coalesce returns the first value that isn't null or undefined (a ?? b ?? c), or Undefined.
func Coalesce(vals ...any) any {
	for _, v := range vals {
		if !IsNullish(v) {
			return v
		}
	}
	if len(vals) > 0 {
		return vals[len(vals)-1]
	}
	return Undefined
}

// Or returns the first truthy value (a || b || c), or the last value.
func Or(vals ...any) any {
	for _, v := range vals {
		if Truthy(v) {
			return v
		}
	}
	if len(vals) > 0 {
		return vals[len(vals)-1]
	}
	return Undefined
}

// OrUndefined is JavaScript's `v || undefined`.
func OrUndefined(v any) any {
	if Truthy(v) {
		return v
	}
	return Undefined
}

// If returns a when cond is true, otherwise b (cond ? a : b).
func If(cond bool, a, b any) any {
	if cond {
		return a
	}
	return b
}

// Truthy implements JavaScript truthiness.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil, undefinedType:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	case int:
		return x != 0
	case int64:
		return x != 0
	}
	return true
}

// Num returns v as a float64 if it is a JSON number.
func Num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float32:
		return float64(x), true
	}
	return 0, false
}

// IsNumber reports whether v is a number (typeof v === "number").
func IsNumber(v any) bool {
	_, ok := Num(v)
	return ok
}

// Str returns v as a string if it is one.
func Str(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// Arr returns v as an array if it is one.
func Arr(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, true
	case []*Obj:
		out := make([]any, len(x))
		for i, o := range x {
			out[i] = o
		}
		return out, true
	}
	return nil, false
}

// AsObj returns v as an object if it is one (arrays are not objects here).
func AsObj(v any) (*Obj, bool) {
	o, ok := v.(*Obj)
	return o, ok && o != nil
}

// Len implements v?.length for arrays and strings (UTF-16 code units).
func Len(v any) (int, bool) {
	if a, ok := Arr(v); ok {
		return len(a), true
	}
	if s, ok := v.(string); ok {
		return utf16Len(s), true
	}
	return 0, false
}

// HasLength is JavaScript's `v?.length` used as a condition.
func HasLength(v any) bool {
	n, ok := Len(v)
	return ok && n > 0
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// StrictEq implements === for JSON values (objects and arrays compare by identity).
func StrictEq(a, b any) bool {
	if fa, ok := Num(a); ok {
		fb, ok := Num(b)
		return ok && fa == fb
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case undefinedType:
		return IsUndefined(b)
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case *Obj:
		y, ok := b.(*Obj)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		return ok && len(x) == len(y) && len(x) > 0 && &x[0] == &y[0]
	}
	return false
}

// Includes is Array.prototype.includes (SameValueZero for JSON values).
func Includes(list []any, v any) bool {
	for _, x := range list {
		if StrictEq(x, v) {
			return true
		}
		if f, ok := Num(x); ok && math.IsNaN(f) {
			if g, ok := Num(v); ok && math.IsNaN(g) {
				return true
			}
		}
	}
	return false
}

// ToNumber implements JavaScript's Number(v).
func ToNumber(v any) float64 {
	if f, ok := Num(v); ok {
		return f
	}
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		return stringToNumber(x)
	case []any:
		if len(x) == 0 {
			return 0
		}
		if len(x) == 1 {
			return stringToNumber(String(x[0]))
		}
	}
	return math.NaN()
}

func stringToNumber(s string) float64 {
	s = strings.TrimFunc(s, isJSSpace)
	if s == "" {
		return 0
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			n, err := strconv.ParseUint(s[2:], base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(n)
		}
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r == '.' || r == 'e' || r == 'E' || r == '+' || r == '-') {
			return math.NaN()
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f
		}
		return math.NaN()
	}
	return f
}

func isJSSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// ParseInt implements JavaScript's Number.parseInt(s, 10): leading whitespace,
// an optional sign and as many decimal digits as follow. ok is false for NaN.
func ParseInt(s string) (float64, bool) {
	s = strings.TrimLeftFunc(s, isJSSpace)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return math.NaN(), false
	}
	f, _ := strconv.ParseFloat(s[:end], 64)
	if neg {
		f = -f
	}
	return f, true
}

// String implements String(v) / template literal conversion.
func String(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case undefinedType:
		return "undefined"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		return x
	case *Obj:
		return "[object Object]"
	}
	if f, ok := Num(v); ok {
		return FormatNumber(f)
	}
	if a, ok := Arr(v); ok {
		parts := make([]string, len(a))
		for i, e := range a {
			if !IsNullish(e) {
				parts[i] = String(e)
			}
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// FormatNumber formats a number like JavaScript's Number.prototype.toString.
func FormatNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs < 1e-6 || abs >= 1e21 {
		format = 'e'
	}
	b := strconv.AppendFloat(nil, f, format, -1, 64)
	if format == 'e' {
		// e-07 -> e-7, as JavaScript writes it.
		n := len(b)
		if n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return string(b)
}

// Round implements Math.round (halves round towards +Infinity).
func Round(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	r := math.Floor(x)
	if x-r >= 0.5 {
		r++
	}
	return r
}

// LocaleCompare approximates String.prototype.localeCompare in the root
// locale for the ASCII strings it's used on: punctuation sorts before digits,
// digits before letters, letters compare case-insensitively first and
// lower case sorts before upper case on a tie.
func LocaleCompare(a, b string) int {
	weight := func(r rune) (int, int) {
		switch {
		case r >= 'a' && r <= 'z':
			return 3000 + int(r-'a'), 0
		case r >= 'A' && r <= 'Z':
			return 3000 + int(r-'A'), 1
		case r >= '0' && r <= '9':
			return 2000 + int(r-'0'), 0
		case r < 128:
			return 1000 + int(r), 0
		}
		return 4000 + int(r), 0
	}
	ra, rb := []rune(a), []rune(b)
	tie := 0
	for i := 0; i < len(ra) && i < len(rb); i++ {
		wa, ca := weight(ra[i])
		wb, cb := weight(rb[i])
		if wa != wb {
			if wa < wb {
				return -1
			}
			return 1
		}
		if tie == 0 && ca != cb {
			if ca < cb {
				tie = -1
			} else {
				tie = 1
			}
		}
	}
	switch {
	case len(ra) < len(rb):
		return -1
	case len(ra) > len(rb):
		return 1
	}
	return tie
}

// SortStable sorts list with a JavaScript-style comparator (negative: a first).
func SortStable(list []any, cmp func(a, b any) float64) {
	sort.SliceStable(list, func(i, j int) bool { return cmp(list[i], list[j]) < 0 })
}
