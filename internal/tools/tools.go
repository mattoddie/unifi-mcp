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
	// audit is the UNIFI_AUDIT_LOG writer, or nil.
	audit *auditLog
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
	// RevealsSecrets marks a tool whose purpose is to return a secret (such as
	// unifi_get_wifi_qr); it is only registered with UNIFI_ALLOW_SECRETS.
	RevealsSecrets bool
	Input          []Prop
	Handler        func(ctx context.Context, a Args) (any, error)
	// Reads, for a tool that changes things, reports whether these arguments
	// only read (unifi_api_request with GET); such calls aren't audited.
	Reads func(a Args) bool
}

func boolPtr(b bool) *bool { return &b }

// readOnly is the tool's readOnlyHint: Gate == Read unless ReadOnly overrides it.
func (d Def) readOnly() bool {
	if d.ReadOnly != nil {
		return *d.ReadOnly
	}
	return d.Gate == Read
}

// site returns the site argument or the configured default.
func (c *Context) site(a Args) string {
	if s, ok := a.Get("site").(string); ok {
		return s
	}
	return c.Client.DefaultSite()
}

// enabled reports whether a tool's toolset and gate are enabled.
func (c *Context) enabled(d Def) bool {
	return c.Toolsets[d.Toolset] &&
		(d.Gate == Read || c.AllowWrites) &&
		(d.Gate != Delete || c.AllowDeletes) &&
		(!d.RevealsSecrets || c.AllowSecrets)
}

// includeSecrets reports whether a call asked for include_secrets and
// UNIFI_ALLOW_SECRETS permits it.
func (c *Context) includeSecrets(a Args) bool {
	return c.AllowSecrets && a.Truthy("include_secrets")
}

// add registers a tool if its toolset and gate are enabled.
func (c *Context) add(d Def) {
	if !c.enabled(d) {
		return
	}
	// Tools that change things get dry_run and the audit log here, around the runner.
	var guard *writeGuard
	if !d.readOnly() {
		guard = c.guardWrites(&d)
	}
	tool, run, schema := c.build(d)
	c.Server.AddTool(tool, handler(guard.wrap(run)))
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
	readOnly := d.readOnly()
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
	return text[:cut] + fmt.Sprintf("\n\n[Output truncated: the result is %d bytes, more than the %d-byte limit. Narrow the request, for example with search, fields, a filter, a limit or a specific id.]", len(text), MaxOutputBytes)
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
