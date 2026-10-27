package tools

import (
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
	}
	// A client that still sends include_secrets gets redacted output.
	wlans := env.list("unifi_list_wlans", `{"include_secrets":true}`)
	eq(t, "summary passphrase", get(wlans, 0, "passphrase"), nil)
	raw := env.list("unifi_list_wlans", `{"raw":true,"include_secrets":true}`)
	eq(t, "raw passphrase", get(raw, 0, "x_passphrase"), "[redacted]")
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
