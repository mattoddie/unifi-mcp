package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
)

func TestOnlyReadToolsWhenWritesDisabled(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	tools := toolMap(t, env.mcp)
	if tools["unifi_list_devices"] == nil || tools["unifi_block_client"] != nil {
		t.Errorf("read-only tool set wrong")
	}
	for name, tool := range tools {
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not read-only", name)
		}
	}
}

func TestListDevicesSummary(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	res := env.call("unifi_list_devices", `{"type":"switch"}`)
	eq(t, "total", get(res, "total"), 1.0)
	eq(t, "summary", get(res, "items", 0), js(`{"id":"d1","name":"Office Switch","mac":"aa:bb:cc:00:00:01","ip":"10.0.0.2","model":"US8P60","model_code":"US8P60","type":"switch","state":"connected","adopted":true,"firmware":"7.1.26","uptime":"1d 1h 1m","clients":4,"cpu_pct":5.1,"mem_pct":40.2}`))
	if got := env.mock.Last().Header.Get("X-API-KEY"); got != "key123" {
		t.Errorf("api key header %q", got)
	}
	// Output keeps the field order of the TypeScript server.
	raw := text(callRaw(t, env.mcp, "unifi_list_devices", `{"type":"switch"}`))
	if !strings.HasPrefix(raw, "{\n  \"total\": 1,\n  \"returned\": 1,\n  \"items\": [\n    {\n      \"id\": \"d1\",\n      \"name\": \"Office Switch\"") {
		t.Errorf("unexpected formatting:\n%s", raw)
	}
}

func TestRawOutputIsRedacted(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	dev := env.call("unifi_get_device", `{"mac":"AA-BB-CC-00-00-01","raw":true}`)
	eq(t, "x_authkey", get(dev, "x_authkey"), "[redacted]")
	wlans := env.call("unifi_list_wlans", nil)
	eq(t, "no passphrase", get(wlans, 0, "passphrase"), nil)
	withSecrets := env.call("unifi_list_wlans", `{"include_secrets":true}`)
	eq(t, "passphrase", get(withSecrets, 0, "passphrase"), "hunter22")
}

func TestFilterClients(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	res := env.call("unifi_list_clients", `{"connection":"wired"}`)
	eq(t, "total", get(res, "total"), 1.0)
	eq(t, "name", get(res, "items", 0, "name"), "NAS")
	eq(t, "port", get(res, "items", 0, "switch_port"), 3.0)
	search := env.call("unifi_list_clients", `{"search":"LAPTOP"}`)
	eq(t, "rate", get(search, "items", 0, "tx_rate_mbps"), 866.0)
}

func TestToolErrorsAreResults(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	contains(t, "error", env.callErr("unifi_get_site_health", `{"site":"missing"}`), "NoSiteContext")
	contains(t, "prefix", env.callErr("unifi_get_site_health", `{"site":"missing"}`), "Error: GET /api/s/missing/stat/health failed with HTTP 400: api.err.NoSiteContext")
}

func TestToolsetsFilter(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	o := config.Tools{AllowWrites: true, Toolsets: map[string]bool{"clients": true}}
	tools := toolMap(t, connect(t, env.client, o))
	if tools["unifi_list_clients"] == nil || tools["unifi_block_client"] == nil || tools["unifi_list_devices"] != nil {
		t.Errorf("toolset filter: %d tools", len(tools))
	}
	for name := range tools {
		if strings.HasPrefix(name, "unifi_delete_") {
			t.Errorf("%s registered without deletes", name)
		}
	}
}

func TestDeleteToolsNeedDeletes(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	without := toolMap(t, connect(t, env.client, toolOptions(true, false)))
	with := toolMap(t, connect(t, env.client, toolOptions(true, true)))
	for name := range without {
		if strings.HasPrefix(name, "unifi_delete_") {
			t.Errorf("%s registered without deletes", name)
		}
	}
	for name, tool := range with {
		if strings.HasPrefix(name, "unifi_delete_") && (tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Errorf("%s is not destructive", name)
		}
	}
	if len(with) < len(without) {
		t.Error("fewer tools with deletes")
	}
}

func TestWriteTools(t *testing.T) {
	env := startMock(t, toolOptions(true, false))
	block := toolMap(t, env.mcp)["unifi_block_client"]
	if block == nil || !*block.Annotations.DestructiveHint {
		t.Fatal("unifi_block_client should be destructive")
	}
	env.call("unifi_block_client", `{"mac":"11-22-33-44-55-66"}`)
	eq(t, "block body", env.mock.Last().Body, js(`{"cmd":"block-sta","mac":"11:22:33:44:55:66"}`))

	updated := env.call("unifi_update_client", `{"mac":"11:22:33:44:55:66","name":"Work laptop","fixed_ip":"10.0.0.60"}`)
	eq(t, "update body", env.mock.Last().Body, js(`{"name":"Work laptop","use_fixedip":true,"fixed_ip":"10.0.0.60","network_id":"n1"}`))
	eq(t, "name", get(updated, "name"), "Work laptop")
	eq(t, "fixed ip", get(updated, "fixed_ip"), "10.0.0.60")

	env.call("unifi_power_cycle_port", `{"mac":"aa:bb:cc:00:00:01","port_idx":4}`)
	eq(t, "power cycle", env.mock.Last().Body, js(`{"cmd":"power-cycle","mac":"aa:bb:cc:00:00:01","port_idx":4}`))
}

func TestGatesAndAnnotations(t *testing.T) {
	var reg []Info
	NewServer(nil, config.AllTools(), &reg)
	env := startMock(t, toolOptions(false, false))
	reads := toolMap(t, env.mcp)
	writes := toolMap(t, connect(t, env.client, toolOptions(true, false)))
	all := toolMap(t, connect(t, env.client, toolOptions(true, true)))
	if len(reg) != 132 || len(all) != 132 || len(reads) != 54 || len(writes) != 107 {
		t.Errorf("counts: registered %d, all %d, read %d, write %d", len(reg), len(all), len(reads), len(writes))
	}
	for _, info := range reg {
		_, inRead := reads[info.Name]
		_, inWrite := writes[info.Name]
		switch info.Gate {
		case Read:
			if !inRead {
				t.Errorf("%s missing in read-only mode", info.Name)
			}
		case Write:
			if inRead || !inWrite {
				t.Errorf("%s: write gating (read=%v write=%v)", info.Name, inRead, inWrite)
			}
		case Delete:
			if inRead || inWrite {
				t.Errorf("%s registered without deletes", info.Name)
			}
		}
		tool := all[info.Name]
		if tool.Title == "" || tool.Description == "" || tool.Annotations.Title != tool.Title {
			t.Errorf("%s: title/description", info.Name)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s: openWorldHint should be false", info.Name)
		}
		if info.Gate == Delete && !*tool.Annotations.DestructiveHint {
			t.Errorf("%s: delete tool not destructive", info.Name)
		}
		if tool.Annotations.ReadOnlyHint && tool.Annotations.DestructiveHint != nil {
			t.Errorf("%s: read-only tool with destructiveHint", info.Name)
		}
	}
	// The raw tool follows the write switch.
	if !reads["unifi_api_request"].Annotations.ReadOnlyHint || all["unifi_api_request"].Annotations.ReadOnlyHint {
		t.Error("unifi_api_request annotations")
	}
	contains(t, "raw read-only description", reads["unifi_api_request"].Description, "Only GET is allowed")
	contains(t, "raw all description", all["unifi_api_request"].Description, "All HTTP methods are allowed.")
}

func TestInstructions(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	contains(t, "read-only", env.mcp.InitializeResult().Instructions, "The server is read-only; write tools are disabled (UNIFI_ALLOW_WRITES=false).")
	rw := connect(t, env.client, toolOptions(true, false))
	contains(t, "writes", rw.InitializeResult().Instructions, "Write tools are enabled: confirm with the user")
	if got := rw.InitializeResult().ServerInfo; got.Name != "unifi-mcp" || got.Version != Version {
		t.Errorf("server info %+v", got)
	}
}

func TestInputValidation(t *testing.T) {
	env := startMock(t, toolOptions(true, true))
	before := len(env.mock.Requests())
	for _, c := range []struct{ tool, args, want string }{
		{"unifi_list_devices", `{"type":"bogus"}`, `MCP error -32602: Input validation error: Invalid arguments for tool unifi_list_devices: Invalid option: expected one of "access_point"|"switch"|"gateway"|"other" at type`},
		{"unifi_restart_device", `{}`, `Invalid input: expected string, received undefined at mac`},
		{"unifi_list_clients", `{"limit":1.5}`, `Invalid input: expected int, received number at limit`},
		{"unifi_list_clients", `{"limit":0}`, `Too small: expected number to be >0 at limit`},
		{"unifi_forget_clients", `{"macs":[]}`, `Too small: expected array to have >=1 items at macs`},
		{"unifi_forget_clients", `{"macs":[1]}`, `Invalid input: expected string, received number at macs[0]`},
		{"unifi_update_device", `{"mac":5,"led_color":"red","led_brightness":101,"ip_config":{"type":"x"}}`, "Invalid input: expected string, received number at mac\nInvalid string: must match pattern /^#(?:[0-9a-fA-F]{3}){1,2}$/ at led_color\nToo big: expected number to be <=100 at led_brightness\nInvalid option: expected one of \"dhcp\"|\"static\" at ip_config.type"},
		{"unifi_save_wlan", `{"name":"","passphrase":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","bands":[],"mac_filter":{"policy":"allow"}}`, "Too small: expected string to have >=1 characters at name\nToo big: expected string to have <=63 characters at passphrase\nToo small: expected array to have >=1 items at bands\nInvalid input: expected array, received undefined at mac_filter.macs"},
		{"unifi_set_radio", `{"mac":"a","radio":"ng","channel":"x","channel_width":30,"min_rssi":-30}`, "Invalid input at channel\nInvalid input at channel_width\nToo big: expected number to be <=-40 at min_rssi"},
		{"unifi_upgrade_device", `{"mac":"a","firmware_url":"notaurl"}`, "Invalid URL at firmware_url"},
		{"unifi_invite_admin", `{"name":"a","email":"bad"}`, "Invalid email address at email"},
		{"unifi_save_vpn", `{"dns":["a","b","c"]}`, "Too big: expected array to have <=2 items at dns"},
		{"unifi_update_country_blocking", `{"countries":["usa"]}`, "Too big: expected string to have exactly 2 characters at countries[0]"},
		{"unifi_list_devices", `{"raw":"yes","site":null}`, "Invalid input: expected string, received null at site\nInvalid input: expected boolean, received string at raw"},
		{"unifi_save_firewall_policy", `{"source":{"zone_id":1},"schedule":{}}`, "Invalid input: expected string, received number at source.zone_id\nInvalid input: expected string, received undefined at schedule.mode"},
		{"unifi_save_radius_profile", `{"auth_servers":[{"ip":"x"}]}`, "Invalid input: expected string, received undefined at auth_servers[0].secret"},
		{"unifi_api_request", `{"path":"/x","query":{"a":1}}`, "Invalid input: expected string, received number at query.a"},
		{"unifi_list_threats", `{"hours":0}`, "Too small: expected number to be >0 at hours"},
	} {
		res := callRaw(t, env.mcp, c.tool, c.args)
		if !res.IsError || !strings.HasSuffix(text(res), c.want) {
			t.Errorf("%s %s:\n got  %q\n want %q", c.tool, c.args, text(res), c.want)
		}
	}
	if len(env.mock.Requests()) != before {
		t.Error("invalid arguments reached the controller")
	}
	// Unknown keys are dropped, and nullable fields accept null.
	env.call("unifi_list_devices", `{"bogus":1}`)
	env.call("unifi_save_wlan", `{"id":"w1","min_rate_2g_kbps":null}`)
	eq(t, "nullable", get(lastBody(env.mock, "PUT"), "minrate_ng_enabled"), false)
}

func TestPrompts(t *testing.T) {
	env := startMock(t, toolOptions(true, true))
	var names []string
	for p, err := range env.mcp.Prompts(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, p.Name)
	}
	for _, want := range []string{"network_health_check", "troubleshoot_client", "security_review", "firmware_review", "wifi_optimization"} {
		if !strings.Contains(strings.Join(names, ","), want) {
			t.Errorf("missing prompt %s", want)
		}
	}
	res, err := env.mcp.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "troubleshoot_client", Arguments: map[string]string{"client": "laptop", "site": "branch"}})
	if err != nil {
		t.Fatal(err)
	}
	txt := res.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{`"laptop"`, `site "branch"`, "unifi_get_client"} {
		contains(t, "prompt", txt, want)
	}
	health, _ := env.mcp.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "network_health_check"})
	if txt := health.Messages[0].Content.(*mcp.TextContent).Text; !strings.HasPrefix(txt, "Do a health check of my UniFi network.\n1. unifi_get_site_health") {
		t.Errorf("no-site prompt: %q", txt)
	}
	if _, err := env.mcp.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "troubleshoot_client"}); err == nil || !strings.Contains(err.Error(), "expected string, received undefined at client") {
		t.Errorf("missing required argument: %v", err)
	}
}

func TestEveryArgumentIsDescribedOrDeliberatelyBare(t *testing.T) {
	var reg []Info
	NewServer(nil, config.AllTools(), &reg)
	names := map[string]bool{}
	for _, info := range reg {
		if names[info.Name] {
			t.Errorf("duplicate tool %s", info.Name)
		}
		names[info.Name] = true
		if !strings.HasPrefix(info.Name, "unifi_") {
			t.Errorf("tool name %s", info.Name)
		}
		if info.Schema.Get("type") != "object" || info.Schema.Get("$schema") != "http://json-schema.org/draft-07/schema#" {
			t.Errorf("%s: schema header", info.Name)
		}
	}
}
