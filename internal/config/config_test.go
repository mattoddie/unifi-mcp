package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var base = map[string]string{"UNIFI_URL": "https://10.0.0.1", "UNIFI_API_KEY": "k", "UNIFI_VERIFY_SSL": "false"}

func load(t *testing.T, env map[string]string, extra ...string) (*Config, error) {
	t.Helper()
	m := map[string]string{}
	for k, v := range env {
		m[k] = v
	}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return Load(MapEnv(m))
}

func mustLoad(t *testing.T, env map[string]string, extra ...string) *Config {
	t.Helper()
	cfg, err := load(t, env, extra...)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRequiresURLAndCredentials(t *testing.T) {
	if _, err := load(t, nil); err == nil || err.Error() != "UNIFI_URL is required (e.g. https://192.168.1.1)" {
		t.Errorf("no URL: %v", err)
	}
	if _, err := load(t, map[string]string{"UNIFI_URL": "https://10.0.0.1"}); err == nil || err.Error() != "Set either UNIFI_API_KEY or both UNIFI_USERNAME and UNIFI_PASSWORD" {
		t.Errorf("no credentials: %v", err)
	}
	if _, err := load(t, map[string]string{"UNIFI_URL": "https://10.0.0.1", "UNIFI_USERNAME": "u"}); err == nil {
		t.Error("username without password")
	}
	if _, err := load(t, map[string]string{"UNIFI_URL": "not a url", "UNIFI_API_KEY": "k"}); err == nil || err.Error() != "UNIFI_URL is not a valid URL: not a url" {
		t.Errorf("bad URL: %v", err)
	}
	mustLoad(t, map[string]string{"UNIFI_URL": "http://10.0.0.1", "UNIFI_USERNAME": "u", "UNIFI_PASSWORD": "p"})
}

func TestDefaults(t *testing.T) {
	cfg := mustLoad(t, map[string]string{"UNIFI_URL": "https://10.0.0.1/", "UNIFI_API_KEY": "k", "UNIFI_VERIFY_SSL": "false"})
	u := cfg.Unifi
	if u.URL != "https://10.0.0.1" || u.Site != "default" || u.VerifySSL || u.ControllerType != "auto" || u.TimeoutMs != 30000 || u.CAFile != "" || u.TLSFingerprints != nil {
		t.Errorf("unifi defaults: %+v", u)
	}
	if cfg.Tools.AllowWrites || cfg.Tools.AllowDeletes || cfg.Tools.AllowSecrets || len(cfg.Tools.Toolsets) != len(Toolsets) {
		t.Errorf("tool defaults: %+v", cfg.Tools)
	}
	h := cfg.HTTP
	if cfg.Transport != "stdio" || h.Port != 8080 || h.Host != "127.0.0.1" || h.AuthToken != "" || h.AllowUnauthenticated || h.AllowedHosts != nil {
		t.Errorf("transport defaults: %s %+v", cfg.Transport, h)
	}
	if mustLoad(t, base, "UNIFI_URL", "https://h///").Unifi.URL != "https://h" {
		t.Error("trailing slashes")
	}
}

func TestGatingAndToolsets(t *testing.T) {
	if _, err := load(t, base, "UNIFI_ALLOW_DELETES", "true"); err == nil || err.Error() != "UNIFI_ALLOW_DELETES=true also requires UNIFI_ALLOW_WRITES=true" {
		t.Errorf("deletes without writes: %v", err)
	}
	cfg := mustLoad(t, base, "UNIFI_ALLOW_WRITES", "true", "UNIFI_ALLOW_DELETES", "YES", "UNIFI_TOOLSETS", "overview, Clients,,")
	var sets []string
	for s := range cfg.Tools.Toolsets {
		sets = append(sets, s)
	}
	sort.Strings(sets)
	if !cfg.Tools.AllowDeletes || !reflect.DeepEqual(sets, []string{"clients", "overview"}) {
		t.Errorf("toolsets: %v", sets)
	}
	if _, err := load(t, base, "UNIFI_TOOLSETS", "overview,bogus"); err == nil || !strings.HasPrefix(err.Error(), `Unknown toolset "bogus" in UNIFI_TOOLSETS; valid: all, overview, devices,`) {
		t.Errorf("bad toolset: %v", err)
	}
	// Booleans are lenient, as before: anything other than 1/true/yes/on is false.
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		if !mustLoad(t, base, "UNIFI_ALLOW_WRITES", v).Tools.AllowWrites {
			t.Errorf("%q should enable writes", v)
		}
	}
	for _, v := range []string{"0", "false", "maybe", "off"} {
		if mustLoad(t, base, "UNIFI_ALLOW_WRITES", v).Tools.AllowWrites {
			t.Errorf("%q should not enable writes", v)
		}
	}
	// UNIFI_ALLOW_SECRETS is independent of writes.
	if !mustLoad(t, base, "UNIFI_ALLOW_SECRETS", "true").Tools.AllowSecrets {
		t.Error("UNIFI_ALLOW_SECRETS=true should allow secrets")
	}
	if mustLoad(t, base, "UNIFI_ALLOW_SECRETS", "false", "UNIFI_ALLOW_WRITES", "true").Tools.AllowSecrets {
		t.Error("writes should not allow secrets")
	}
}

func TestValidation(t *testing.T) {
	for _, c := range []struct{ k, v, want string }{
		{"UNIFI_CONTROLLER_TYPE", "cloud", `UNIFI_CONTROLLER_TYPE must be auto, unifi-os or legacy, got "cloud"`},
		{"UNIFI_CONTROLLER_TYPE", "legacy", "API keys are only supported by UniFi OS consoles; use UNIFI_USERNAME/UNIFI_PASSWORD for legacy controllers"},
		{"MCP_TRANSPORT", "sse", `MCP_TRANSPORT must be stdio or http, got "sse"`},
		{"UNIFI_TIMEOUT_MS", "-1", `UNIFI_TIMEOUT_MS must be a positive integer, got "-1"`},
		{"UNIFI_TIMEOUT_MS", "abc", `UNIFI_TIMEOUT_MS must be a positive integer, got "abc"`},
		{"MCP_HTTP_PORT", "http", `MCP_HTTP_PORT must be a positive integer, got "http"`},
		{"UNIFI_TLS_FINGERPRINT", "abc", `UNIFI_TLS_FINGERPRINT: "abc" is not a SHA-256 certificate fingerprint (64 hex digits, optionally colon-separated)`},
	} {
		if _, err := load(t, base, c.k, c.v); err == nil || err.Error() != c.want {
			t.Errorf("%s=%s: %v", c.k, c.v, err)
		}
	}
	// parseInt semantics: leading digits count.
	if mustLoad(t, base, "UNIFI_TIMEOUT_MS", "5000ms").Unifi.TimeoutMs != 5000 {
		t.Error("parseInt-style timeout")
	}
	cfg := mustLoad(t, map[string]string{"UNIFI_URL": "http://x", "UNIFI_USERNAME": "u", "UNIFI_PASSWORD": "p", "UNIFI_CONTROLLER_TYPE": "legacy", "UNIFI_SITE": "branch"})
	if cfg.Unifi.ControllerType != "legacy" || cfg.Unifi.Site != "branch" {
		t.Errorf("legacy: %+v", cfg.Unifi)
	}
}

func TestHTTPAuthRequirement(t *testing.T) {
	_, err := load(t, base, "MCP_TRANSPORT", "http")
	if err == nil || err.Error() != "MCP_AUTH_TOKEN is required for the http transport (generate one with: openssl rand -hex 32); set MCP_ALLOW_UNAUTHENTICATED=true only behind something else that authenticates" {
		t.Errorf("http without token: %v", err)
	}
	if _, err := load(t, base, "MCP_TRANSPORT", "http", "MCP_ALLOW_UNAUTHENTICATED", "true"); err == nil || err.Error() != UnauthenticatedNeedsHosts {
		t.Errorf("unauthenticated without allowed hosts: %v", err)
	}
	if !mustLoad(t, base, "MCP_TRANSPORT", "http", "MCP_ALLOW_UNAUTHENTICATED", "true", "MCP_ALLOWED_HOSTS", "mcp.example.lan").HTTP.AllowUnauthenticated {
		t.Error("MCP_ALLOW_UNAUTHENTICATED")
	}
	mustLoad(t, base) // stdio needs no token
	if _, err := load(t, base, "MCP_TRANSPORT", "http", "MCP_AUTH_TOKEN", strings.Repeat("t", 15)); err == nil || err.Error() != "MCP_AUTH_TOKEN must be at least 16 characters (generate one with: openssl rand -hex 32)" {
		t.Errorf("short token: %v", err)
	}
	cfg := mustLoad(t, base, "MCP_TRANSPORT", "http", "MCP_AUTH_TOKEN", strings.Repeat("t", 16), "MCP_ALLOWED_HOSTS", "MCP.Example.LAN, Other:8080", "MCP_HTTP_PORT", "9000")
	if cfg.HTTP.Host != "127.0.0.1" || cfg.HTTP.Port != 9000 || !reflect.DeepEqual(cfg.HTTP.AllowedHosts, []string{"mcp.example.lan", "other:8080"}) {
		t.Errorf("http: %+v", cfg.HTTP)
	}
}

func TestFileSecrets(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("  from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, map[string]string{"UNIFI_URL": "http://x", "UNIFI_API_KEY": "env", "UNIFI_API_KEY_FILE": key, "MCP_AUTH_TOKEN_FILE": key})
	if cfg.Unifi.APIKey != "from-file" || cfg.HTTP.AuthToken != "from-file" {
		t.Errorf("_FILE: %q %q", cfg.Unifi.APIKey, cfg.HTTP.AuthToken)
	}
	missing := filepath.Join(dir, "missing")
	if _, err := load(t, map[string]string{"UNIFI_URL": "http://x", "UNIFI_API_KEY_FILE": missing}); err == nil || err.Error() != "Could not read UNIFI_API_KEY_FILE ("+missing+"): ENOENT: no such file or directory, open '"+missing+"'" {
		t.Errorf("missing file: %v", err)
	}
	// An empty _FILE variable falls back to the plain one.
	if mustLoad(t, map[string]string{"UNIFI_URL": "http://x", "UNIFI_API_KEY": "env", "UNIFI_API_KEY_FILE": ""}).Unifi.APIKey != "env" {
		t.Error("empty _FILE")
	}
}

// An MCP Bundle host sets every variable in the manifest: optional settings the
// user left empty arrive as "" (the manifest's default) or, in some Claude
// Desktop versions, as the literal ${user_config.<key>}. Both mean unset.
func TestBundleUnsetValues(t *testing.T) {
	fp := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, unset := range []func(key string) string{
		func(string) string { return "" },
		func(key string) string { return "${user_config." + key + "}" },
	} {
		env := map[string]string{
			"UNIFI_URL":             "https://10.0.0.1",
			"UNIFI_API_KEY":         "k",
			"UNIFI_USERNAME":        unset("username"),
			"UNIFI_PASSWORD":        unset("password"),
			"UNIFI_SITE":            unset("site"),
			"UNIFI_TLS_FINGERPRINT": fp,
			"UNIFI_VERIFY_SSL":      unset("verify_ssl"),
			"UNIFI_CA_FILE":         unset("ca_file"),
			"UNIFI_ALLOW_WRITES":    "false",
			"UNIFI_ALLOW_DELETES":   "false",
			"UNIFI_TOOLSETS":        unset("toolsets"),
		}
		cfg := mustLoad(t, env)
		u := cfg.Unifi
		if u.Site != "default" || u.VerifySSL || u.CAFile != "" || u.Username != "" || u.Password != "" || len(u.TLSFingerprints) != 1 {
			t.Errorf("%q: unifi: %+v", env["UNIFI_SITE"], u)
		}
		if cfg.Tools.AllowWrites || cfg.Tools.AllowDeletes || len(cfg.Tools.Toolsets) != len(Toolsets) {
			t.Errorf("%q: tools: %+v", env["UNIFI_SITE"], cfg.Tools)
		}
		// With nothing to check the certificate, an https controller is still refused.
		env["UNIFI_TLS_FINGERPRINT"] = unset("tls_fingerprint")
		if _, err := load(t, env); err == nil || err.Error() != TLSChoiceRequired {
			t.Errorf("%q: no TLS choice: %v", env["UNIFI_TLS_FINGERPRINT"], err)
		}
		// Username and password stand in for an unset API key.
		env["UNIFI_TLS_FINGERPRINT"], env["UNIFI_API_KEY"], env["UNIFI_USERNAME"], env["UNIFI_PASSWORD"] = fp, unset("api_key"), "u", "p"
		if cfg := mustLoad(t, env); cfg.Unifi.APIKey != "" || cfg.Unifi.Username != "u" {
			t.Errorf("%q: password auth: %+v", env["UNIFI_API_KEY"], cfg.Unifi)
		}
	}
	// Only a whole placeholder is treated as unset.
	if cfg := mustLoad(t, base, "UNIFI_SITE", "x${user_config.site}"); cfg.Unifi.Site != "x${user_config.site}" {
		t.Errorf("partial placeholder: %q", cfg.Unifi.Site)
	}
}

func TestTLSSettings(t *testing.T) {
	fp := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, in := range []string{fp, strings.ToLower(strings.ReplaceAll(fp, ":", "")), "sha256:" + fp, "SHA-256=" + strings.ReplaceAll(fp, ":", "-")} {
		if got, err := NormalizeFingerprint(in); err != nil || got != fp {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	wrong := strings.Repeat("00", 32)
	cfg := mustLoad(t, base, "UNIFI_TLS_FINGERPRINT", strings.ToLower(strings.ReplaceAll(fp, ":", ""))+", "+wrong)
	wrongNorm, _ := NormalizeFingerprint(wrong)
	if !reflect.DeepEqual(cfg.Unifi.TLSFingerprints, []string{fp, wrongNorm}) {
		t.Errorf("pins: %v", cfg.Unifi.TLSFingerprints)
	}

	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, []byte("not checked here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(t, base, "UNIFI_CA_FILE", filepath.Join(dir, "missing.pem")); err == nil || !strings.HasPrefix(err.Error(), "Could not read UNIFI_CA_FILE") {
		t.Errorf("missing CA: %v", err)
	}
	if !mustLoad(t, base, "UNIFI_VERIFY_SSL", "", "UNIFI_CA_FILE", ca).Unifi.VerifySSL {
		t.Error("a CA file turns verification on")
	}
	if _, err := load(t, base, "UNIFI_CA_FILE", ca, "UNIFI_VERIFY_SSL", "false"); err == nil || err.Error() != "UNIFI_CA_FILE needs certificate verification; remove UNIFI_VERIFY_SSL=false" {
		t.Errorf("CA without verification: %v", err)
	}
}

func TestInsecureTLSWarning(t *testing.T) {
	if w := InsecureTLSWarning(mustLoad(t, base).Unifi); !strings.Contains(w, "UNIFI_TLS_FINGERPRINT") {
		t.Errorf("warning: %q", w)
	}
	fp := strings.Repeat("ab", 32)
	for _, extra := range [][]string{{"UNIFI_TLS_FINGERPRINT", fp}, {"UNIFI_VERIFY_SSL", "true"}, {"UNIFI_URL", "http://10.0.0.1"}} {
		if w := InsecureTLSWarning(mustLoad(t, base, extra...).Unifi); w != "" {
			t.Errorf("%v: unexpected warning", extra)
		}
	}
}

func TestAllTools(t *testing.T) {
	all := AllTools()
	if !all.AllowWrites || !all.AllowDeletes || !all.AllowSecrets || len(all.Toolsets) != 14 {
		t.Errorf("AllTools: %+v", all)
	}
}

func TestToolModeRemoved(t *testing.T) {
	const want = "UNIFI_TOOL_MODE was removed in 0.6.0: every enabled tool is listed. Use UNIFI_TOOLSETS to keep the list small (see docs/configuration.md)."
	for _, v := range []string{"lazy", "full", "Full", "eager", "0"} {
		if _, err := load(t, base, "UNIFI_TOOL_MODE", v); err == nil || err.Error() != want {
			t.Errorf("UNIFI_TOOL_MODE=%q: %v", v, err)
		}
	}
	// Unset, blank, or the placeholder an older MCP Bundle passes for an empty field.
	for _, v := range []string{"", "  ", "${user_config.tool_mode}"} {
		if _, err := load(t, base, "UNIFI_TOOL_MODE", v); err != nil {
			t.Errorf("UNIFI_TOOL_MODE=%q: %v", v, err)
		}
	}
	mustLoad(t, base)
}

func TestHealthcheckURL(t *testing.T) {
	dir := t.TempDir()
	portFile := filepath.Join(dir, "port")
	if err := os.WriteFile(portFile, []byte("9100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"MCP_TRANSPORT": "stdio"}, ""},
		{map[string]string{}, "http://127.0.0.1:8080/health"},
		{map[string]string{"MCP_HTTP_HOST": "0.0.0.0", "MCP_HTTP_PORT": "9000"}, "http://127.0.0.1:9000/health"},
		{map[string]string{"MCP_HTTP_HOST": "::"}, "http://127.0.0.1:8080/health"},
		{map[string]string{"MCP_HTTP_HOST": "192.0.2.10"}, "http://192.0.2.10:8080/health"},
		{map[string]string{"MCP_HTTP_HOST": "::1"}, "http://[::1]:8080/health"},
		{map[string]string{"MCP_HTTP_PORT_FILE": portFile}, "http://127.0.0.1:9100/health"},
	} {
		if got, err := HealthcheckURL(MapEnv(c.env)); err != nil || got != c.want {
			t.Errorf("%v: %q %v, want %q", c.env, got, err, c.want)
		}
	}
	if _, err := HealthcheckURL(MapEnv(map[string]string{"MCP_HTTP_PORT": "x"})); err == nil {
		t.Error("bad port")
	}
}

func TestTLSChoiceRequired(t *testing.T) {
	https := map[string]string{"UNIFI_URL": "https://10.0.0.1", "UNIFI_API_KEY": "k"}
	if _, err := load(t, https); err == nil || err.Error() != TLSChoiceRequired {
		t.Errorf("https without a TLS choice: %v", err)
	}
	for _, s := range []string{"UNIFI_TLS_FINGERPRINT", "UNIFI_CA_FILE", "UNIFI_VERIFY_SSL"} {
		if !strings.Contains(TLSChoiceRequired, s) {
			t.Errorf("error should mention %s", s)
		}
	}
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{
		{"UNIFI_VERIFY_SSL", "false"},
		{"UNIFI_VERIFY_SSL", "true"},
		{"UNIFI_TLS_FINGERPRINT", strings.Repeat("ab", 32)},
		{"UNIFI_CA_FILE", ca},
		{"UNIFI_URL", "http://10.0.0.1"},
	} {
		if _, err := load(t, https, extra...); err != nil {
			t.Errorf("%v: %v", extra, err)
		}
	}
	// A pin alone doesn't switch chain verification on.
	if mustLoad(t, https, "UNIFI_TLS_FINGERPRINT", strings.Repeat("ab", 32)).Unifi.VerifySSL {
		t.Error("pin turned verification on")
	}
}

func TestPlainHTTPWarning(t *testing.T) {
	if w := PlainHTTPWarning(mustLoad(t, base, "UNIFI_URL", "HTTP://10.0.0.1").Unifi); !strings.Contains(w, "unencrypted") {
		t.Errorf("warning: %q", w)
	}
	if w := PlainHTTPWarning(mustLoad(t, base).Unifi); w != "" {
		t.Errorf("https: %q", w)
	}
}

func TestURLWithUserinfoIsRejected(t *testing.T) {
	for _, u := range []string{"https://admin:secret@10.0.0.1", "https://admin@10.0.0.1"} {
		_, err := load(t, base, "UNIFI_URL", u)
		if err == nil || !strings.Contains(err.Error(), "must not contain a username or password") || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestAuditLog(t *testing.T) {
	if cfg := mustLoad(t, base); cfg.Tools.AuditLog != "" {
		t.Errorf("audit log on by default: %q", cfg.Tools.AuditLog)
	}
	if cfg := mustLoad(t, base, "UNIFI_AUDIT_LOG", "stderr"); cfg.Tools.AuditLog != AuditLogStderr {
		t.Errorf("stderr: %q", cfg.Tools.AuditLog)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	if cfg := mustLoad(t, base, "UNIFI_AUDIT_LOG", path); cfg.Tools.AuditLog != path {
		t.Errorf("path: %q", cfg.Tools.AuditLog)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("created %v, %v", info, err)
	}
	_, err := load(t, base, "UNIFI_AUDIT_LOG", filepath.Join(dir, "missing", "audit.log"))
	if err == nil || !strings.Contains(err.Error(), "UNIFI_AUDIT_LOG: cannot write to") || !strings.Contains(err.Error(), "no such file or directory") {
		t.Errorf("missing directory: %v", err)
	}
	if _, err := load(t, base, "UNIFI_AUDIT_LOG", dir); err == nil {
		t.Error("a directory was accepted as the audit log")
	}
}
