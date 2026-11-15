package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/config"
)

// noSecrets is every toolset, read-only, without UNIFI_ALLOW_SECRETS: the defaults.
func noSecrets() config.Tools {
	o := toolOptions(false, false)
	o.AllowSecrets = false
	return o
}

func TestSecretsNeedAllowSecrets(t *testing.T) {
	env := startMock(t, noSecrets())
	tools := toolMap(t, env.mcp)
	if tools["unifi_get_wifi_qr"] != nil {
		t.Error("unifi_get_wifi_qr registered without UNIFI_ALLOW_SECRETS")
	}
	for name, tool := range tools {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(schema), "include_secrets") {
			t.Errorf("%s offers include_secrets without UNIFI_ALLOW_SECRETS", name)
		}
		if strings.Contains(tool.Description, "include_secrets") {
			t.Errorf("%s description mentions include_secrets without UNIFI_ALLOW_SECRETS: %q", name, tool.Description)
		}
	}
	// A client that still sends include_secrets gets redacted output.
	wlans := env.list("unifi_list_wlans", `{"include_secrets":true}`)
	eq(t, "summary passphrase", get(wlans, 0, "passphrase"), nil)
	raw := env.list("unifi_list_wlans", `{"raw":true,"include_secrets":true}`)
	eq(t, "raw passphrase", get(raw, 0, "x_passphrase"), "[redacted]")
}

// A tool that adds include_secrets to its input without going through the
// helpers still doesn't offer or receive it: build drops it in one place.
func TestBuildDropsSecretsArg(t *testing.T) {
	for _, allow := range []bool{false, true} {
		o := noSecrets()
		o.AllowSecrets = allow
		c := &Context{Tools: o}
		var got bool
		tool, run, _ := c.build(Def{
			Toolset: "overview", Name: "test_tool",
			Input: props(siteArg(), P("include_secrets", Bool().Opt())),
			Handler: func(_ context.Context, a Args) (any, error) {
				got = a.Truthy("include_secrets")
				return nil, nil
			},
		})
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if offered := strings.Contains(string(schema), "include_secrets"); offered != allow {
			t.Errorf("allow=%v: schema offers include_secrets = %v", allow, offered)
		}
		if res := run(context.Background(), json.RawMessage(`{"include_secrets":true}`)); res.IsError {
			t.Fatalf("allow=%v: call failed: %+v", allow, res.Content)
		}
		if got != allow {
			t.Errorf("allow=%v: handler received include_secrets = %v", allow, got)
		}
	}
}

func TestSecretsWithAllowSecrets(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	tools := toolMap(t, env.mcp)
	if tools["unifi_get_wifi_qr"] == nil {
		t.Error("unifi_get_wifi_qr missing with UNIFI_ALLOW_SECRETS")
	}
	raw := env.list("unifi_list_wlans", `{"raw":true,"include_secrets":true}`)
	eq(t, "raw passphrase", get(raw, 0, "x_passphrase"), "hunter22")
	// Without include_secrets, output is still redacted.
	eq(t, "raw redacted", get(env.list("unifi_list_wlans", `{"raw":true}`), 0, "x_passphrase"), "[redacted]")
}
