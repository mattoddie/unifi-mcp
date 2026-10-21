package tools

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

// Lazy mode (UNIFI_TOOL_MODE=lazy) lists only a few meta-tools, so clients that
// load every tool into the model's context don't spend 40-130 KB on them. The
// real tools are kept in a registry with the same gates, annotations, schemas
// and validation as in full mode, and are reached through the meta-tools.
//
// Reads and writes go through separate meta-tools so client-side confirmation
// still means something: unifi_call_tool is read-only and never prompts, and
// unifi_call_write_tool is destructive, so clients ask before every call.

// Names of the meta-tools.
const (
	searchToolName    = "unifi_search_tools"
	callToolName      = "unifi_call_tool"
	callWriteToolName = "unifi_call_write_tool"
)

// searchLimit caps the tools a keyword search returns.
const searchLimit = 15

// lazyTool is a tool held behind the meta-tools.
type lazyTool struct {
	def    Def
	tool   *mcp.Tool
	run    runner
	schema *Obj
	// name, title and desc are the search terms in each.
	name, title, desc map[string]bool
}

func (t *lazyTool) readOnly() bool { return t.tool.Annotations.ReadOnlyHint }

func (t *lazyTool) destructive() bool {
	d := t.tool.Annotations.DestructiveHint
	return d != nil && *d
}

// access is read, write or delete, as the tool reference labels it.
func (t *lazyTool) access() string {
	if t.readOnly() {
		return "read"
	}
	if t.def.Gate == Delete {
		return "delete"
	}
	return "write"
}

// caller is the meta-tool that runs this tool.
func (t *lazyTool) caller() string {
	if t.readOnly() {
		return callToolName
	}
	return callWriteToolName
}

type registry struct {
	tools  []*lazyTool
	byName map[string]*lazyTool
	// off says why each known tool that isn't enabled is missing.
	off map[string]string
}

func newRegistry() *registry {
	return &registry{byName: map[string]*lazyTool{}, off: map[string]string{}}
}

func (r *registry) add(d Def, tool *mcp.Tool, run runner, schema *Obj) {
	t := &lazyTool{def: d, tool: tool, run: run, schema: schema, name: wordSet(d.Name), title: wordSet(d.Title), desc: wordSet(d.Description)}
	r.tools = append(r.tools, t)
	r.byName[d.Name] = t
}

// ---------------------------------------------------------------- search

// stopWords are left out of searches: every tool name starts with unifi_.
var stopWords = map[string]bool{"unifi": true, "a": true, "an": true, "the": true, "of": true, "for": true, "to": true, "and": true, "or": true, "on": true, "in": true, "my": true, "with": true, "all": true, "tool": true, "tools": true}

// synonyms map words people use to the words the tools use.
var synonyms = map[string][]string{
	"wifi": {"wlan"}, "wireless": {"wlan"}, "ssid": {"wlan"}, "wlan": {"wifi"},
	"vlan": {"network"}, "ap": {"access", "point"}, "poe": {"power"},
	"remove": {"delete"}, "create": {"save"}, "update": {"save"}, "edit": {"save"}, "change": {"save"},
	"reboot": {"restart"}, "ban": {"block"}, "kick": {"reconnect"}, "guest": {"hotspot"},
	"show": {"list", "get"}, "find": {"list", "get"},
}

// stem folds simple English plurals so "switches" finds "switch".
func stem(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 4 && (strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes") || strings.HasSuffix(w, "sses") || strings.HasSuffix(w, "xes")):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

// words splits text into lower-case, stemmed search terms.
func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !stopWords[w] {
			out = append(out, stem(w))
		}
	}
	return out
}

func wordSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range words(s) {
		set[w] = true
	}
	return set
}

// score rates a tool against the query terms: a match in the name counts most,
// then the title, then the description. matched is how many terms matched.
func (t *lazyTool) score(terms []string) (matched, score int) {
	for _, term := range terms {
		best := 0
		for _, w := range append([]string{term}, synonyms[term]...) {
			w = stem(w)
			s := 0
			switch {
			case t.name[w]:
				s = 6
			case t.title[w]:
				s = 4
			case t.desc[w]:
				s = 2
			case len(w) >= 4 && hasPrefix(t.name, w):
				s = 3
			case len(w) >= 4 && hasPrefix(t.desc, w):
				s = 1
			}
			if w != stem(term) {
				s /= 2 // a synonym counts for less
			}
			best = max(best, s)
		}
		if best > 0 {
			matched++
			score += best
		}
	}
	return matched, score
}

func hasPrefix(set map[string]bool, prefix string) bool {
	for w := range set {
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

// search returns the tools matching the query, best first, optionally in one toolset.
func (r *registry) search(query, toolset string) []*lazyTool {
	terms := words(query)
	type hit struct {
		t              *lazyTool
		matched, score int
	}
	var hits []hit
	for _, t := range r.tools {
		if toolset != "" && t.def.Toolset != toolset {
			continue
		}
		if len(terms) == 0 {
			hits = append(hits, hit{t: t})
			continue
		}
		if m, s := t.score(terms); m > 0 {
			hits = append(hits, hit{t, m, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].matched != hits[j].matched {
			return hits[i].matched > hits[j].matched
		}
		return hits[i].score > hits[j].score
	})
	out := make([]*lazyTool, len(hits))
	for i, h := range hits {
		out[i] = h.t
	}
	return out
}

// closest suggests up to three enabled tools for a name that doesn't exist.
func (r *registry) closest(name string) []any {
	var out []any
	for _, t := range r.search(name, "") {
		if len(out) == 3 {
			break
		}
		out = append(out, t.def.Name)
	}
	return out
}

// summary is the first sentence of a description.
func summary(desc string) string {
	s := desc
	for i := 0; i+1 < len(s); i++ {
		if (s[i] == '.' || s[i] == ':') && s[i+1] == ' ' {
			s = s[:i+1]
			break
		}
	}
	if len(s) > 200 {
		cut := strings.LastIndex(s[:200], " ")
		s = s[:max(cut, 150)] + "…"
	}
	return s
}

func (t *lazyTool) brief() *Obj {
	return ojson.New("name", t.def.Name, "title", t.def.Title, "toolset", t.def.Toolset, "access", t.access(), "destructive", t.destructive(), "summary", summary(t.def.Description))
}

func (t *lazyTool) full() *Obj {
	a := t.tool.Annotations
	ann := ojson.New("readOnlyHint", a.ReadOnlyHint)
	if a.DestructiveHint != nil {
		ann.Set("destructiveHint", *a.DestructiveHint)
	}
	return ojson.New("name", t.def.Name, "title", t.def.Title, "toolset", t.def.Toolset, "access", t.access(),
		"call_with", t.caller(), "annotations", ann, "description", t.def.Description, "input_schema", t.schema)
}

// ---------------------------------------------------------------- meta-tools

// registerMeta registers the meta-tools that reach the tools in c.lazy.
func registerMeta(c *Context) {
	r := c.lazy
	var toolsets []string
	for _, ts := range config.Toolsets {
		if slices.ContainsFunc(r.tools, func(t *lazyTool) bool { return t.def.Toolset == ts }) {
			toolsets = append(toolsets, ts)
		}
	}
	callers := callToolName
	if c.AllowWrites {
		callers += " (reads) or " + callWriteToolName + " (changes)"
	}

	toolsetArg := Str()
	if len(toolsets) > 0 {
		toolsetArg = Enum(toolsets...)
	}
	c.meta(Def{
		Name: searchToolName, Title: "Search UniFi tools",
		Description: fmt.Sprintf("Find UniFi Network tools (%d available) and their arguments. Call with: query and/or toolset to list matching tools; "+
			"names to get each tool's description and input schema before calling it with %s; nothing to list the toolsets.", len(r.tools), callers),
		Input: props(
			P("query", Str().Opt().D("Keywords, e.g. 'switch port poe'")),
			P("toolset", toolsetArg.Opt().D("Only tools in this toolset")),
			P("names", Array(Str()).Min(1).Max(20).Opt().D("Exact tool names to describe in full")),
		),
		Handler: func(_ context.Context, a Args) (any, error) {
			if names := a.Arr("names"); len(names) > 0 {
				return r.describe(names), nil
			}
			query, toolset := a.Str("query"), a.Str("toolset")
			if query == "" && toolset == "" {
				return r.overview(toolsets, callers), nil
			}
			found := r.search(query, toolset)
			total := len(found)
			if query != "" && total > searchLimit {
				found = found[:searchLimit]
			}
			out := ojson.New("total", total, "returned", len(found), "tools", ojson.Map(anys(found), func(t any) any { return t.(*lazyTool).brief() }))
			if total == 0 {
				out.Set("hint", "No match. Try other keywords, or call "+searchToolName+" with no arguments to see the toolsets.")
			} else {
				out.Set("next", "Call "+searchToolName+" with names to get a tool's input schema, then "+callers+".")
			}
			return out, nil
		},
	})

	refuseWrites := "Tools that change state are disabled (UNIFI_ALLOW_WRITES=false)."
	if c.AllowWrites {
		refuseWrites = "Tools that change state are refused; use " + callWriteToolName + "."
	}
	c.meta(Def{
		Name: callToolName, Title: "Call a read-only UniFi tool",
		Description: "Run a read-only UniFi tool found with " + searchToolName + ", e.g. {\"name\":\"unifi_list_devices\",\"arguments\":{\"type\":\"switch\"}}. " +
			"Get its input schema with " + searchToolName + " names first unless you already know it. " + refuseWrites,
		Input: props(
			P("name", Str().MinLen(1).D("Tool name")),
			P("arguments", Record(nil).Opt().D("The tool's arguments, as its input schema describes")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			return r.call(ctx, a, true, c.AllowWrites), nil
		},
	})

	if c.AllowWrites {
		c.meta(Def{
			Name: callWriteToolName, Title: "Call a UniFi tool that changes state",
			Description: "Run a UniFi tool that changes state (configures, restarts, blocks, deletes…), found with " + searchToolName + ". " +
				"Before every call, tell the user exactly what will change and get their confirmation. " +
				"Get the tool's input schema with " + searchToolName + " names first. Read-only tools are refused; use " + callToolName + ".",
			Destructive: boolPtr(true),
			Input: props(
				P("name", Str().MinLen(1).D("Tool name")),
				P("arguments", Record(nil).Opt().D("The tool's arguments, as its input schema describes")),
			),
			Handler: func(ctx context.Context, a Args) (any, error) {
				return r.call(ctx, a, false, true), nil
			},
		})
	}
}

// meta registers a meta-tool directly on the server. Gate Read with a
// Destructive override gives a destructive, non-read-only tool without needing
// a toolset.
func (c *Context) meta(d Def) {
	if d.Destructive != nil {
		d.ReadOnly = boolPtr(false)
	}
	tool, run, _ := c.build(d)
	c.Server.AddTool(tool, handler(run))
}

func anys(list []*lazyTool) []any {
	out := make([]any, len(list))
	for i, t := range list {
		out[i] = t
	}
	return out
}

// overview lists the toolsets with their tool counts and a few example tools.
func (r *registry) overview(toolsets []string, callers string) *Obj {
	list := make([]any, 0, len(toolsets))
	for _, ts := range toolsets {
		// Read tools make the best examples; writes fill any gap.
		var reads, writes []any
		for _, t := range r.tools {
			if t.def.Toolset != ts {
				continue
			}
			if t.readOnly() {
				reads = append(reads, t.def.Name)
			} else {
				writes = append(writes, t.def.Name)
			}
		}
		count := len(reads) + len(writes)
		names := append(reads, writes...)[:min(3, count)]
		list = append(list, ojson.New("toolset", ts, "tools", count, "examples", names))
	}
	return ojson.New("total", len(r.tools), "toolsets", list,
		"next", "Search with a query or a toolset, get schemas with names, then "+callers+".")
}

// describe returns the full definition of each named tool.
func (r *registry) describe(names []any) *Obj {
	found, missing := []any{}, []any{}
	for _, n := range names {
		name, _ := n.(string)
		if t := r.byName[name]; t != nil {
			found = append(found, t.full())
		} else {
			missing = append(missing, r.unknown(name))
		}
	}
	out := ojson.New("tools", found)
	if len(missing) > 0 {
		out.Set("unknown", missing)
	}
	return out
}

// unknown explains a tool name that can't be called.
func (r *registry) unknown(name string) string {
	if reason, ok := r.off[name]; ok {
		return fmt.Sprintf("%s is not available because %s.", name, reason)
	}
	msg := fmt.Sprintf("Unknown tool %q.", name)
	if s := r.closest(name); len(s) > 0 {
		parts := make([]string, len(s))
		for i, n := range s {
			parts[i] = n.(string)
		}
		msg += " Did you mean " + strings.Join(parts, ", ") + "?"
	}
	return msg + " Use " + searchToolName + " to find tools."
}

// call runs a tool for unifi_call_tool (reads) or unifi_call_write_tool (writes).
func (r *registry) call(ctx context.Context, a Args, reads, writesEnabled bool) *mcp.CallToolResult {
	name := a.Str("name")
	t := r.byName[name]
	if t == nil {
		return fail("Error: " + r.unknown(name))
	}
	if reads && !t.readOnly() {
		if !writesEnabled {
			return fail("Error: " + name + " changes state and writes are disabled (UNIFI_ALLOW_WRITES=false).")
		}
		return fail("Error: " + name + " changes state, so " + callToolName + " can't run it. Confirm the change with the user, then call it with " + callWriteToolName + ".")
	}
	if !reads && t.readOnly() {
		return fail("Error: " + name + " is read-only; call it with " + callToolName + " instead.")
	}
	raw, _ := ojson.Stringify(ojson.Coalesce(a.Get("arguments"), ojson.New()))
	return t.run(ctx, []byte(raw))
}
