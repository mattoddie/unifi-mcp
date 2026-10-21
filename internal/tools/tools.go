// Package tools defines unifi-mcp's MCP tools and prompts.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// Obj is a JSON object.
type Obj = ojson.Obj

// U is undefined, for leaving a field out of an object.
var U = ojson.Undefined

// Gate is the setting a tool needs before it is registered.
type Gate int

const (
	Read   Gate = iota // always registered
	Write              // UNIFI_ALLOW_WRITES
	Delete             // UNIFI_ALLOW_WRITES and UNIFI_ALLOW_DELETES
)

func (g Gate) String() string {
	return [...]string{"read", "write", "delete"}[g]
}

// Context is shared by the tool definitions.
type Context struct {
	Server *mcp.Server
	Client *unifi.Client
	config.Tools
	// Registered records each registered tool in order, for the docs generator.
	Registered *[]Info
	// lazy holds the tools behind the meta-tools in lazy mode (nil in full mode).
	lazy *registry
}

// Info describes a registered tool.
type Info struct {
	Name    string
	Toolset string
	Gate    Gate
	// Schema is the tool's input schema, with its properties in order.
	Schema *ojson.Obj
}

// Def describes a tool.
type Def struct {
	Toolset     string
	Name        string
	Title       string
	Description string
	Gate        Gate
	// ReadOnly overrides the readOnlyHint annotation (defaults to Gate == Read).
	ReadOnly *bool
	// Destructive overrides the destructiveHint annotation (defaults to Gate == Delete).
	Destructive *bool
	Input       []Prop
	Handler     func(ctx context.Context, a Args) (any, error)
}

func boolPtr(b bool) *bool { return &b }

// site returns the site argument or the configured default.
func (c *Context) site(a Args) string {
	if s, ok := a.Get("site").(string); ok {
		return s
	}
	return c.Client.DefaultSite()
}

// disabledReason says why a tool isn't registered, or returns "" when its toolset and gate are enabled.
func (c *Context) disabledReason(d Def) string {
	switch {
	case !c.Toolsets[d.Toolset]:
		return fmt.Sprintf("its toolset %q is not enabled in UNIFI_TOOLSETS", d.Toolset)
	case d.Gate != Read && !c.AllowWrites:
		return "it changes state and writes are disabled (UNIFI_ALLOW_WRITES=false)"
	case d.Gate == Delete && !c.AllowDeletes:
		return "it deletes configuration and deletes are disabled (UNIFI_ALLOW_DELETES=false)"
	}
	return ""
}

// add registers a tool if its toolset and gate are enabled: directly in full
// mode, or in the registry behind the meta-tools in lazy mode.
func (c *Context) add(d Def) {
	if reason := c.disabledReason(d); reason != "" {
		if c.lazy != nil {
			c.lazy.off[d.Name] = reason
		}
		return
	}
	tool, run, schema := c.build(d)
	if c.lazy != nil {
		c.lazy.add(d, tool, run, schema)
	} else {
		c.Server.AddTool(tool, handler(run))
	}
	if c.Registered != nil {
		*c.Registered = append(*c.Registered, Info{Name: d.Name, Toolset: d.Toolset, Gate: d.Gate, Schema: schema})
	}
}

// runner runs a tool with its raw JSON arguments, validating them first.
type runner func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult

func handler(run runner) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return run(ctx, req.Params.Arguments), nil
	}
}

// build makes the MCP tool (annotations and input schema) and its runner.
func (c *Context) build(d Def) (*mcp.Tool, runner, *ojson.Obj) {
	readOnly := d.Gate == Read
	if d.ReadOnly != nil {
		readOnly = *d.ReadOnly
	}
	ann := &mcp.ToolAnnotations{Title: d.Title, ReadOnlyHint: readOnly, OpenWorldHint: boolPtr(false)}
	if !readOnly {
		ann.DestructiveHint = boolPtr(d.Gate == Delete)
		if d.Destructive != nil {
			ann.DestructiveHint = d.Destructive
		}
	}
	schema := inputSchema(d.Input)
	raw, _ := ojson.Stringify(schema)
	tool := &mcp.Tool{Name: d.Name, Title: d.Title, Description: d.Description, Annotations: ann, InputSchema: json.RawMessage(raw)}
	input := Object(d.Input...)
	run := func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var args any = ojson.New()
		if len(raw) > 0 && strings.TrimSpace(string(raw)) != "null" {
			parsed, err := ojson.Parse(raw)
			if err != nil {
				return fail("MCP error -32602: Input validation error: Invalid arguments for tool " + d.Name + ": " + err.Error())
			}
			args = parsed
		}
		var issues []issue
		parsed := input.parse(args, nil, &issues)
		if len(issues) > 0 {
			msgs := make([]string, len(issues))
			for i, is := range issues {
				msgs[i] = is.String()
			}
			return fail("MCP error -32602: Input validation error: Invalid arguments for tool " + d.Name + ": " + strings.Join(msgs, "\n"))
		}
		obj, _ := parsed.(*Obj)
		out, err := d.Handler(ctx, Args{obj})
		if err != nil {
			return fail("Error: " + err.Error())
		}
		// The meta-tools pass on the result of the tool they called.
		if res, isResult := out.(*mcp.CallToolResult); isResult {
			return res
		}
		return ok(out)
	}
	return tool, run, schema
}

// MaxOutputBytes caps the text a tool returns, so one huge response (a raw API call,
// or a list on a very large site) can't flood the model's context.
const MaxOutputBytes = 256 << 10

func ok(data any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: truncateOutput(ojson.StringifyIndent(data))}}}
}

// truncateOutput cuts text longer than MaxOutputBytes at a character boundary and says so.
func truncateOutput(text string) string {
	if len(text) <= MaxOutputBytes {
		return text
	}
	cut := MaxOutputBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + fmt.Sprintf("\n\n[Output truncated: the result is %d bytes, more than the %d-byte limit. Narrow the request, for example with a filter, a limit or a specific id.]", len(text), MaxOutputBytes)
}

func fail(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: true}
}

// Args are a tool's validated arguments.
type Args struct{ o *Obj }

// Get returns an argument, or Undefined when it wasn't given.
func (a Args) Get(k string) any { return a.o.Get(k) }

// Has reports whether an argument was given (it may be null).
func (a Args) Has(k string) bool { return a.o.Has(k) }

// Str returns a string argument ("" when not given).
func (a Args) Str(k string) string {
	s, _ := a.o.Get(k).(string)
	return s
}

// Truthy is the argument's JavaScript truthiness.
func (a Args) Truthy(k string) bool { return ojson.Truthy(a.o.Get(k)) }

// Num returns a number argument.
func (a Args) Num(k string) (float64, bool) { return ojson.Num(a.o.Get(k)) }

// NumOr returns a number argument, or fallback when it wasn't given.
func (a Args) NumOr(k string, fallback float64) float64 {
	if f, ok := ojson.Num(a.o.Get(k)); ok {
		return f
	}
	return fallback
}

// Arr returns an array argument (nil when not given).
func (a Args) Arr(k string) []any {
	arr, _ := ojson.Arr(a.o.Get(k))
	return arr
}

// Obj returns an object argument (nil when not given or null).
func (a Args) Obj(k string) *Obj {
	o, _ := ojson.AsObj(a.o.Get(k))
	return o
}

// errorf builds a plain error.
func errorf(msg string) error { return errors.New(msg) }

// mac normalises a MAC address argument.
func mac(v any) (string, error) {
	s, _ := v.(string)
	return unifi.NormalizeMac(s)
}

// macs normalises a list of MAC addresses.
func macs(list []any) ([]any, error) {
	out := make([]any, len(list))
	for i, m := range list {
		n, err := mac(m)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

// list returns v as an array of objects; non-objects are kept as nil entries skipped by callers.
func objs(v any) []*Obj {
	arr, _ := ojson.Arr(v)
	out := make([]*Obj, 0, len(arr))
	for _, e := range arr {
		if o, ok := ojson.AsObj(e); ok {
			out = append(out, o)
		} else {
			out = append(out, ojson.New())
		}
	}
	return out
}

// arr returns v as an array, or an empty one.
func arr(v any) []any {
	a, _ := ojson.Arr(v)
	if a == nil {
		return []any{}
	}
	return a
}

// mapObjs maps f over v's elements (treated as objects).
func mapObjs(v any, f func(*Obj) any) []any {
	list := objs(v)
	out := make([]any, len(list))
	for i, o := range list {
		out[i] = f(o)
	}
	return out
}

// filterAndLimit applies a case-insensitive substring search over some fields, then a limit.
func filterAndLimit(items []any, search string, limit *float64, fields func(*Obj) []any) *Obj {
	out := items
	if search != "" {
		q := strings.ToLower(search)
		out = ojson.Filter(items, func(item any) bool {
			o, _ := ojson.AsObj(item)
			for _, f := range fields(o) {
				if !ojson.IsNullish(f) && strings.Contains(strings.ToLower(ojson.String(f)), q) {
					return true
				}
			}
			return false
		})
	}
	total := len(out)
	if limit != nil && int(*limit) < len(out) {
		out = out[:int(*limit)]
	}
	return ojson.New("total", total, "returned", len(out), "items", out)
}

// optLimit returns a pointer to a limit argument or its default.
func optLimit(a Args, def float64) *float64 {
	v := a.NumOr("limit", def)
	return &v
}
