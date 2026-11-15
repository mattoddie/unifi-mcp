package tools

// Shared handling for list tools. Every unifi_list_* tool returns
// {total, returned, items} and accepts the same narrowing arguments, so a
// model can ask for exactly what it needs:
//
//   - search: case-insensitive substring match on a few useful fields
//   - limit:  maximum number of items (only on lists that can grow long)
//   - fields: keep only these fields in each item (dot paths for nested ones)
//   - exact-match filters declared per tool (for example a device's state)
//
// To add a list tool, declare a listing, put listing.args() in the tool's Input
// and return listing.result() from the handler. See docs/development.md.

import (
	"fmt"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// listing describes how a list tool narrows its results.
type listing struct {
	// Search lists the fields matched by the search argument, as dot paths.
	// They are looked up in both the summary and the controller object.
	// Empty means every top-level string field of the summary.
	Search []string
	// Filters maps a tool argument to the summary field it must equal
	// (case-insensitively for strings). The tool declares the argument itself.
	Filters map[string]string
	// Limit is the default for the limit argument. Zero means the tool has no
	// limit argument and returns every match; use it for short configuration lists.
	Limit int
}

// filterFetch is how many items to ask the controller for when it applies the
// limit itself but search or filters will narrow the results afterwards.
const filterFetch = 3000

// The list arguments carry no per-tool description, because they appear on
// every list tool and tools/list would grow by kilobytes. The server
// instructions (listHelp) explain them once.

func searchArg() Prop { return P("search", Str().Opt()) }

func limitArg(def int) Prop {
	return P("limit", Int().Positive().Max(5000).Opt().D(fmt.Sprintf("Default %d", def)))
}

func fieldsArg() Prop { return P("fields", Array(Str()).Opt()) }

// ListArgDocs describes the list arguments for docs/tools.md, which has room
// for the descriptions tools/list leaves out.
var ListArgDocs = map[string]string{
	"search": "Case-insensitive substring match on names, MACs, IPs and similar fields.",
	"fields": "Keep only these fields in each item. Dot paths such as `uplink.name` reach nested fields.",
	"limit":  "Maximum number of items to return.",
}

// listHelp is the server instructions' explanation of the list arguments.
const listHelp = "List tools return {total, returned, items} and take `search` (case-insensitive substring match on names, MACs, IPs and similar), " +
	"`fields` (keep only these fields in each item; dot paths like uplink.name) and, on long lists, `limit`. " +
	"Use them to fetch only what you need. "

// args are the list arguments to put in the tool's Input: search, limit (when
// the listing has a default) and fields.
func (l listing) args() []Prop {
	if l.Limit > 0 {
		return []Prop{searchArg(), limitArg(l.Limit), fieldsArg()}
	}
	return []Prop{searchArg(), fieldsArg()}
}

// limit is the limit argument or the listing's default (0: unlimited).
func (l listing) limit(a Args) int {
	if l.Limit == 0 {
		return 0
	}
	return int(a.NumOr("limit", float64(l.Limit)))
}

// fetch is how many items to request from an endpoint that takes a limit:
// the limit itself, or more when search or filters will drop some afterwards.
func (l listing) fetch(a Args) float64 {
	n := l.limit(a)
	if l.narrows(a) && n < filterFetch {
		n = filterFetch
	}
	return float64(n)
}

// narrows reports whether search or a filter was given.
func (l listing) narrows(a Args) bool {
	if a.Str("search") != "" {
		return true
	}
	for arg := range l.Filters {
		if !ojson.IsNullish(a.Get(arg)) {
			return true
		}
	}
	return false
}

// result builds a list tool's output from the controller objects in items.
// summarize turns an object into what the tool shows (nil: the object as is).
// raw, when the raw argument is set and raw is non-nil, gives the object to
// return instead of the summary (for example with secrets redacted).
//
// Search and filters see the summaries; the limit applies after them, and
// fields last, to whatever is returned.
func (l listing) result(a Args, items []any, summarize func(*Obj) any, raw func(*Obj) any) *Obj {
	type pair struct {
		src  *Obj
		item any
	}
	q := strings.ToLower(a.Str("search"))
	var kept []pair
	for _, it := range items {
		src, _ := ojson.AsObj(it)
		var item any = it
		if summarize != nil {
			if src == nil {
				src = ojson.New()
			}
			item = summarize(src)
		}
		if q != "" && !l.matches(item, src, q) {
			continue
		}
		if !l.filtered(a, item) {
			continue
		}
		kept = append(kept, pair{src, item})
	}
	total := len(kept)
	if n := l.limit(a); n > 0 && n < len(kept) {
		kept = kept[:n]
	}
	useRaw := raw != nil && a.Truthy("raw")
	out := make([]any, len(kept))
	for i, p := range kept {
		if useRaw && p.src != nil {
			out[i] = raw(p.src)
		} else {
			out[i] = p.item
		}
	}
	res := ojson.New("total", total, "returned", len(out), "items", out)
	if fields := a.Arr("fields"); len(fields) > 0 {
		projected, hint := selectFields(out, fields)
		res.Set("items", projected)
		if hint != "" {
			res.Set("hint", hint)
		}
	}
	return res
}

// matches reports whether any search field of the summary or the controller
// object contains q (already lower-case).
func (l listing) matches(item any, src *Obj, q string) bool {
	if len(l.Search) == 0 {
		o, ok := ojson.AsObj(item)
		if !ok {
			return containsFold(item, q)
		}
		for _, k := range o.Keys() {
			if s, ok := o.Get(k).(string); ok && strings.Contains(strings.ToLower(s), q) {
				return true
			}
		}
		return false
	}
	for _, f := range l.Search {
		path := strings.Split(f, ".")
		if containsFold(lookup(item, path), q) || (src != nil && containsFold(lookup(src, path), q)) {
			return true
		}
	}
	return false
}

// lookup follows a dot path through objects; arrays along the way are walked
// element by element and give an array of the values found.
func lookup(v any, path []string) any {
	if len(path) == 0 {
		return v
	}
	if list, ok := ojson.Arr(v); ok {
		var out []any
		for _, e := range list {
			if x := lookup(e, path); !ojson.IsUndefined(x) {
				out = append(out, x)
			}
		}
		return out
	}
	o, ok := ojson.AsObj(v)
	if !ok {
		return U
	}
	return lookup(o.Get(path[0]), path[1:])
}

// containsFold reports whether a scalar, or any scalar in an array, contains q.
func containsFold(v any, q string) bool {
	if list, ok := ojson.Arr(v); ok {
		for _, e := range list {
			if containsFold(e, q) {
				return true
			}
		}
		return false
	}
	switch v.(type) {
	case string, float64, bool:
		return strings.Contains(strings.ToLower(ojson.String(v)), q)
	}
	return false
}

// filtered reports whether item passes every exact-match filter that was given.
func (l listing) filtered(a Args, item any) bool {
	for arg, field := range l.Filters {
		want := a.Get(arg)
		if ojson.IsNullish(want) {
			continue
		}
		got := ojson.Get(item, strings.Split(field, ".")...)
		if ws, ok := want.(string); ok {
			gs, ok := got.(string)
			if !ok || !strings.EqualFold(gs, ws) {
				return false
			}
			continue
		}
		if want == false && ojson.IsUndefined(got) {
			// Summaries leave false flags out.
			continue
		}
		if !ojson.StrictEq(got, want) {
			return false
		}
	}
	return true
}

// selectFields keeps only the requested fields in each item. When no item has
// any of them, the hint names the fields the first item does have.
func selectFields(items []any, fields []any) ([]any, string) {
	var paths [][]string
	var names []string
	for _, f := range fields {
		s, _ := f.(string)
		s = strings.Trim(strings.TrimSpace(s), ".")
		if s == "" {
			continue
		}
		paths = append(paths, strings.Split(s, "."))
		names = append(names, s)
	}
	if len(paths) == 0 {
		return items, ""
	}
	out := make([]any, len(items))
	found := false
	for i, it := range items {
		v, ok := pick(it, paths)
		if !ok {
			v = ojson.New()
		}
		found = found || ok
		out[i] = v
	}
	if found || len(items) == 0 {
		return out, ""
	}
	var avail []string
	if o, ok := ojson.AsObj(items[0]); ok {
		avail = o.Keys()
	}
	return out, fmt.Sprintf("None of the requested fields (%s) exist. Available fields: %s.", strings.Join(names, ", "), strings.Join(avail, ", "))
}

// pick copies the paths that exist in v, keeping their nesting. Arrays along a
// path are walked element by element, so radios.channel keeps each radio's channel.
func pick(v any, paths [][]string) (any, bool) {
	if list, ok := ojson.Arr(v); ok {
		out := []any{}
		for _, e := range list {
			if p, ok := pick(e, paths); ok {
				out = append(out, p)
			}
		}
		return out, len(out) > 0
	}
	o, ok := ojson.AsObj(v)
	if !ok {
		return U, false
	}
	out := ojson.New()
	var heads []string
	tails := map[string][][]string{}
	for _, p := range paths {
		if _, seen := tails[p[0]]; !seen {
			heads = append(heads, p[0])
		}
		tails[p[0]] = append(tails[p[0]], p[1:])
	}
	for _, h := range heads {
		if !o.Has(h) {
			continue
		}
		val := o.Get(h)
		whole := false
		var sub [][]string
		for _, t := range tails[h] {
			if len(t) == 0 {
				whole = true
			} else {
				sub = append(sub, t)
			}
		}
		if whole {
			out.Set(h, val)
		} else if p, ok := pick(val, sub); ok {
			out.Set(h, p)
		}
	}
	return out, out.Len() > 0
}

// asIs returns the controller object unchanged, for raw output.
func asIs(o *Obj) any { return o }

// redacted returns the controller object with secrets redacted, for raw output.
func redacted(o *Obj) any { return unifi.RedactSecrets(o) }

// rawWithSecrets returns raw output that redacts secrets unless include_secrets
// is set and UNIFI_ALLOW_SECRETS permits it.
func rawWithSecrets(c *Context, a Args) func(*Obj) any {
	if c.includeSecrets(a) {
		return asIs
	}
	return redacted
}
