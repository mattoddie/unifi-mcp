package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
)

// lazyOptions is every toolset in lazy mode with the given gates.
func lazyOptions(writes, deletes bool) config.Tools {
	o := toolOptions(writes, deletes)
	o.Mode = config.ToolModeLazy
	return o
}

// listSize is the size in bytes of the tools/list result.
func listSize(t *testing.T, s *mcp.ClientSession) int {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func names(m map[string]*mcp.Tool) []string {
	var out []string
	for n := range m {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func TestLazyListsOnlyMetaTools(t *testing.T) {
	env := startMock(t, lazyOptions(false, false))
	read := toolMap(t, env.mcp)
	if got := names(read); !slices.Equal(got, []string{callToolName, searchToolName}) {
		t.Fatalf("read-only meta-tools: %v", got)
	}
	for _, tool := range read {
		if !tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint != nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s annotations: %+v", tool.Name, tool.Annotations)
		}
	}
	contains(t, "read-only call description", read[callToolName].Description, "Tools that change state are disabled")

	all := connect(t, env.client, lazyOptions(true, true))
	tools := toolMap(t, all)
	if got := names(tools); !slices.Equal(got, []string{callToolName, callWriteToolName, searchToolName}) {
		t.Fatalf("meta-tools with writes: %v", got)
	}
	w := tools[callWriteToolName]
	if w.Annotations.ReadOnlyHint || w.Annotations.DestructiveHint == nil || !*w.Annotations.DestructiveHint {
		t.Errorf("call_write_tool must be destructive: %+v", w.Annotations)
	}
	contains(t, "confirm", w.Description, "get their confirmation")
	contains(t, "count", tools[searchToolName].Description, "(145 available)")

	// Well under 4 KB, against 48 KB (read-only) and 174 KB (everything) in full mode.
	readSize, allSize := listSize(t, env.mcp), listSize(t, all)
	fullRead, fullAll := listSize(t, connect(t, env.client, toolOptions(false, false))), listSize(t, connect(t, env.client, toolOptions(true, true)))
	t.Logf("tools/list: lazy %d B read-only, %d B all; full %d B read-only, %d B all", readSize, allSize, fullRead, fullAll)
	if readSize > 3000 || allSize > 3500 {
		t.Errorf("lazy tools/list too big: %d B read-only, %d B all", readSize, allSize)
	}
}

func TestLazyInstructionsAndPrompts(t *testing.T) {
	env := startMock(t, lazyOptions(false, false))
	ins := env.mcp.InitializeResult().Instructions
	for _, want := range []string{searchToolName, callToolName + " {name, arguments}", "unifi_get_site_health", "The server is read-only"} {
		contains(t, "instructions", ins, want)
	}
	if strings.Contains(ins, callWriteToolName) {
		t.Error("read-only instructions mention the write meta-tool")
	}
	contains(t, "write instructions", connect(t, env.client, lazyOptions(true, false)).InitializeResult().Instructions, "tools that change state with "+callWriteToolName)

	res, err := env.mcp.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "network_health_check"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, "prompt", res.Messages[0].Content.(*mcp.TextContent).Text, "reached through "+callToolName)
	full, _ := connect(t, env.client, toolOptions(false, false)).GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "network_health_check"})
	if strings.Contains(full.Messages[0].Content.(*mcp.TextContent).Text, callToolName) {
		t.Error("full-mode prompt mentions the meta-tools")
	}
}

// toolNames returns the names in a search result.
func toolNames(res any) []string {
	var out []string
	for i := 0; ; i++ {
		n, ok := get(res, "tools", i, "name").(string)
		if !ok {
			return out
		}
		out = append(out, n)
	}
}

func TestLazySearch(t *testing.T) {
	env := startMock(t, lazyOptions(true, true))
	for _, c := range []struct{ query, first string }{
		{"site health", "unifi_get_site_health"},
		{"list devices", "unifi_list_devices"},
		{"restart device", "unifi_restart_device"},
		{"switch ports", "unifi_list_switch_ports"},
		{"wifi networks", "unifi_list_wlans"},
		{"delete firewall policy", "unifi_delete_firewall_policy"},
		{"block client", "unifi_block_client"},
	} {
		res := env.call(searchToolName, map[string]any{"query": c.query})
		if got := toolNames(res); len(got) == 0 || got[0] != c.first {
			t.Errorf("%q: got %v, want %s first", c.query, got, c.first)
		}
	}

	res := env.call(searchToolName, `{"query":"list"}`)
	if total, _ := get(res, "total").(float64); total <= searchLimit || get(res, "returned") != float64(searchLimit) {
		t.Errorf("cap: total %v, returned %v", get(res, "total"), get(res, "returned"))
	}
	eq(t, "entry", get(env.call(searchToolName, `{"query":"restart device"}`), "tools", 0),
		js(`{"name":"unifi_restart_device","title":"Restart device","toolset":"devices","access":"write","destructive":true,"summary":"Restart a UniFi device."}`))

	// A toolset on its own lists all of its tools; with a query, only matches in it.
	clients := toolNames(env.call(searchToolName, `{"toolset":"clients"}`))
	if !slices.Contains(clients, "unifi_list_clients") || !slices.Contains(clients, "unifi_block_client") || slices.Contains(clients, "unifi_list_devices") {
		t.Errorf("clients toolset: %v", clients)
	}
	for _, n := range toolNames(env.call(searchToolName, `{"query":"list","toolset":"wifi"}`)) {
		if !strings.Contains(n, "wlan") && !strings.Contains(n, "wifi") {
			t.Errorf("wifi search returned %s", n)
		}
	}
	contains(t, "no match", ojsonString(env.call(searchToolName, `{"query":"zzzz"}`)), "No match")

	overview := env.call(searchToolName, nil)
	eq(t, "total", get(overview, "total"), 145.0)
	eq(t, "first toolset", get(overview, "toolsets", 0, "toolset"), "overview")
	if n, _ := get(overview, "toolsets", 0, "examples").([]any); len(n) == 0 || len(n) > 3 {
		t.Errorf("examples: %v", n)
	}
	contains(t, "bad toolset", text(callRaw(t, env.mcp, searchToolName, `{"toolset":"bogus"}`)), "Invalid option")
}

func ojsonString(v any) string {
	b, _ := json.Marshal(plain(v))
	return string(b)
}

func TestLazyDescribe(t *testing.T) {
	env := startMock(t, lazyOptions(true, false))
	res := env.call(searchToolName, `{"names":["unifi_list_devices","unifi_block_client","unifi_list_device","unifi_delete_network"]}`)
	full := toolMap(t, connect(t, env.client, toolOptions(true, false)))

	eq(t, "name", get(res, "tools", 0, "name"), "unifi_list_devices")
	eq(t, "description", get(res, "tools", 0, "description"), full["unifi_list_devices"].Description)
	eq(t, "call with", get(res, "tools", 0, "call_with"), callToolName)
	schema := full["unifi_list_devices"].InputSchema
	if !same(get(res, "tools", 0, "input_schema"), schema) {
		t.Errorf("schema differs from full mode: %s", ojsonString(get(res, "tools", 0, "input_schema")))
	}
	eq(t, "write", get(res, "tools", 1, "annotations"), js(`{"readOnlyHint":false,"destructiveHint":true}`))
	eq(t, "write call", get(res, "tools", 1, "call_with"), callWriteToolName)
	contains(t, "suggestion", ojsonString(get(res, "unknown", 0)), "Did you mean unifi_list_devices")
	contains(t, "gated", ojsonString(get(res, "unknown", 1)), "deletes are disabled (UNIFI_ALLOW_DELETES=false)")
}

func TestLazyCall(t *testing.T) {
	env := startMock(t, lazyOptions(true, false))
	// Same result as calling the tool directly in full mode.
	res := env.call(callToolName, `{"name":"unifi_list_devices","arguments":{"type":"switch"}}`)
	eq(t, "result", res, callWith(t, connect(t, env.client, toolOptions(false, false)), "unifi_list_devices", `{"type":"switch"}`))
	// Handler errors come back as they do in full mode.
	contains(t, "handler error", env.callErr(callToolName, `{"name":"unifi_get_site_health","arguments":{"site":"missing"}}`), "Error: GET /api/s/missing/stat/health failed")

	// Validation errors are the tool's own.
	for _, c := range []struct{ args, want string }{
		{`{"name":"unifi_list_devices","arguments":{"type":"bogus"}}`, `MCP error -32602: Input validation error: Invalid arguments for tool unifi_list_devices: Invalid option: expected one of "access_point"|"switch"|"gateway"|"other" at type`},
		{`{"name":"unifi_list_clients","arguments":{"limit":1.5}}`, `Invalid arguments for tool unifi_list_clients: Invalid input: expected int, received number at limit`},
		{`{"name":"unifi_restart_device","arguments":{}}`, `Invalid arguments for tool unifi_restart_device: Invalid input: expected string, received undefined at mac`},
		{`{"arguments":{}}`, `Invalid arguments for tool unifi_call_tool: Invalid input: expected string, received undefined at name`},
		{`{"name":"unifi_list_devices","arguments":"{}"}`, `at arguments`},
	} {
		tool := callToolName
		if strings.Contains(c.args, "restart") {
			tool = callWriteToolName
		}
		if got := text(callRaw(t, env.mcp, tool, c.args)); !strings.Contains(got, c.want) {
			t.Errorf("%s:\n got  %q\n want %q", c.args, got, c.want)
		}
	}
	contains(t, "unknown", env.callErr(callToolName, `{"name":"unifi_get_clients"}`), `Unknown tool "unifi_get_clients". Did you mean unifi_get_client`)
}

func TestLazyReadWriteSeparation(t *testing.T) {
	env := startMock(t, lazyOptions(true, false))
	before := len(env.mock.Requests())
	contains(t, "write via read", env.callErr(callToolName, `{"name":"unifi_block_client","arguments":{"mac":"11:22:33:44:55:66"}}`), "call it with "+callWriteToolName)
	contains(t, "read via write", env.callErr(callWriteToolName, `{"name":"unifi_list_devices"}`), "call it with "+callToolName)
	// The raw tool can write when writes are on, so it is a write tool.
	contains(t, "raw via read", env.callErr(callToolName, `{"name":"unifi_api_request","arguments":{"path":"/api/self"}}`), callWriteToolName)
	if len(env.mock.Requests()) != before {
		t.Error("a refused call reached the controller")
	}
	env.call(callWriteToolName, `{"name":"unifi_block_client","arguments":{"mac":"11-22-33-44-55-66"}}`)
	eq(t, "block body", env.mock.Last().Body, js(`{"cmd":"block-sta","mac":"11:22:33:44:55:66"}`))
}

func TestLazyGates(t *testing.T) {
	env := startMock(t, lazyOptions(false, false))
	contains(t, "write off", env.callErr(callToolName, `{"name":"unifi_block_client","arguments":{"mac":"11:22:33:44:55:66"}}`), "unifi_block_client is not available because it changes state and writes are disabled (UNIFI_ALLOW_WRITES=false)")
	if slices.Contains(toolNames(env.call(searchToolName, `{"query":"block client"}`)), "unifi_block_client") {
		t.Error("write tool searchable with writes off")
	}
	// The raw tool is read-only without writes, and refuses anything but GET.
	contains(t, "raw", env.callErr(callToolName, `{"name":"unifi_api_request","arguments":{"method":"POST","path":"/api/s/default/cmd/stamgr"}}`), "Error:")

	writes := connect(t, env.client, lazyOptions(true, false))
	contains(t, "delete off", text(callRaw(t, writes, callWriteToolName, `{"name":"unifi_delete_network","arguments":{"id":"n1"}}`)), "deletes are disabled (UNIFI_ALLOW_DELETES=false)")
	if slices.Contains(toolNames(callWith(t, writes, searchToolName, `{"query":"delete network"}`)), "unifi_delete_network") {
		t.Error("delete tool searchable with deletes off")
	}

	o := lazyOptions(true, true)
	o.Toolsets = map[string]bool{"clients": true}
	clients := connect(t, env.client, o)
	if got := toolNames(callWith(t, clients, searchToolName, `{"query":"devices"}`)); slices.Contains(got, "unifi_list_devices") {
		t.Errorf("disabled toolset searchable: %v", got)
	}
	contains(t, "toolset off", text(callRaw(t, clients, callToolName, `{"name":"unifi_list_devices"}`)), `toolset "devices" is not enabled`)
	overview := callWith(t, clients, searchToolName, nil)
	eq(t, "only clients", get(overview, "toolsets"), js(`[{"toolset":"clients","tools":8,"examples":["unifi_list_clients","unifi_list_known_clients","unifi_get_client"]}]`))
	if len(env.mock.Requests()) != 0 {
		t.Errorf("gated calls reached the controller: %d requests", len(env.mock.Requests()))
	}
}
