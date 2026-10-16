package ojson

import (
	"sort"
	"strconv"
)

// Obj is a JSON object that remembers the order its keys were added in, like
// a JavaScript object. A nil *Obj reads as empty.
type Obj struct {
	keys []string
	vals map[string]any
}

// New builds an object from key/value pairs; Undefined values are skipped.
func New(kv ...any) *Obj {
	o := &Obj{vals: make(map[string]any, len(kv)/2)}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Get returns the value for k, or Undefined when there is none.
func (o *Obj) Get(k string) any {
	if o == nil {
		return Undefined
	}
	if v, ok := o.vals[k]; ok {
		return v
	}
	return Undefined
}

// Has reports whether k is set (k in o).
func (o *Obj) Has(k string) bool {
	if o == nil {
		return false
	}
	_, ok := o.vals[k]
	return ok
}

// Set sets k, keeping its position if it already exists. Setting Undefined removes k.
func (o *Obj) Set(k string, v any) *Obj {
	if IsUndefined(v) {
		o.Delete(k)
		return o
	}
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
	return o
}

// Delete removes k.
func (o *Obj) Delete(k string) {
	if o == nil {
		return
	}
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

// Len is the number of keys (Object.keys(o).length).
func (o *Obj) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Keys returns the keys in JavaScript property order: array-index keys in
// ascending numeric order, then the others in insertion order.
func (o *Obj) Keys() []string {
	if o == nil {
		return nil
	}
	var idx, rest []string
	for _, k := range o.keys {
		if isArrayIndex(k) {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	if len(idx) == 0 {
		return append([]string(nil), o.keys...)
	}
	sort.Slice(idx, func(i, j int) bool {
		a, _ := strconv.ParseUint(idx[i], 10, 64)
		b, _ := strconv.ParseUint(idx[j], 10, 64)
		return a < b
	})
	return append(idx, rest...)
}

func isArrayIndex(k string) bool {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < 4294967295
}

// Assign copies src's keys onto o (Object.assign(o, src)) and returns o.
// A nil or non-object src is ignored.
func (o *Obj) Assign(srcs ...any) *Obj {
	for _, src := range srcs {
		s, ok := AsObj(src)
		if !ok {
			continue
		}
		for _, k := range s.Keys() {
			o.Set(k, s.vals[k])
		}
	}
	return o
}

// Spread builds {...a, ...b, ...}; nil and non-object arguments are ignored.
func Spread(objs ...any) *Obj {
	return New().Assign(objs...)
}

// Without returns a shallow copy of o without the given keys ({k, ...rest} = o).
func (o *Obj) Without(keys ...string) *Obj {
	out := Spread(o)
	for _, k := range keys {
		out.Delete(k)
	}
	return out
}

// Clone deep-copies a JSON value (structuredClone).
func Clone(v any) any {
	switch x := v.(type) {
	case *Obj:
		if x == nil {
			return x
		}
		out := &Obj{keys: append([]string(nil), x.keys...), vals: make(map[string]any, len(x.vals))}
		for k, val := range x.vals {
			out.vals[k] = Clone(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = Clone(e)
		}
		return out
	}
	return v
}

// Get walks nested objects: Get(v, "a", "b") is v?.a?.b. Anything that isn't
// an object along the way yields Undefined.
func Get(v any, path ...string) any {
	for _, k := range path {
		o, ok := AsObj(v)
		if !ok {
			return Undefined
		}
		v = o.Get(k)
	}
	return v
}

// Find returns the first element of list for which pred is true, or Undefined.
func Find(list []any, pred func(any) bool) any {
	for _, e := range list {
		if pred(e) {
			return e
		}
	}
	return Undefined
}

// Filter returns the elements of list for which pred is true.
func Filter(list []any, pred func(any) bool) []any {
	out := []any{}
	for _, e := range list {
		if pred(e) {
			out = append(out, e)
		}
	}
	return out
}

// Map applies f to every element.
func Map(list []any, f func(any) any) []any {
	out := make([]any, len(list))
	for i, e := range list {
		out[i] = f(e)
	}
	return out
}

// Strings converts a []string to a JSON array.
func Strings(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}
