package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

func TestStampServerJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "unifi-mcp-1.2.3-darwin-arm64.mcpb"), "mac")
	write(t, filepath.Join(dir, "unifi-mcp-1.2.3-windows-amd64.mcpb"), "win")
	in := `{"name":"io.github.mattoddie/unifi-mcp","version":"0.0.0-dev","packages":[
		{"registryType":"oci","identifier":"ghcr.io/mattoddie/unifi-mcp:0.0.0-dev","transport":{"type":"stdio"}},
		{"registryType":"mcpb","identifier":"https://github.com/mattoddie/unifi-mcp/releases/download/v0.0.0-dev/unifi-mcp-0.0.0-dev-darwin-arm64.mcpb","fileSha256":"00","transport":{"type":"stdio"}},
		{"registryType":"mcpb","identifier":"https://github.com/mattoddie/unifi-mcp/releases/download/v0.0.0-dev/unifi-mcp-0.0.0-dev-windows-amd64.mcpb","fileSha256":"00","transport":{"type":"stdio"}}]}`
	text, err := stampServerJSON([]byte(in), "1.2.3", dir)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := ojson.AsObj(ojson.MustParse(text))
	if v := s.Get("version"); v != "1.2.3" {
		t.Errorf("version %v", v)
	}
	pkgs := arr(s.Get("packages"))
	for i, want := range []string{
		"ghcr.io/mattoddie/unifi-mcp:1.2.3",
		"https://github.com/mattoddie/unifi-mcp/releases/download/v1.2.3/unifi-mcp-1.2.3-darwin-arm64.mcpb",
		"https://github.com/mattoddie/unifi-mcp/releases/download/v1.2.3/unifi-mcp-1.2.3-windows-amd64.mcpb",
	} {
		if id := ojson.Get(pkgs[i], "identifier"); id != want {
			t.Errorf("package %d identifier %v, want %s", i, id, want)
		}
	}
	for i, body := range map[int]string{1: "mac", 2: "win"} {
		want, _ := sha256File(write(t, filepath.Join(t.TempDir(), "f"), body))
		if got := ojson.Get(pkgs[i], "fileSha256"); got != want {
			t.Errorf("package %d fileSha256 %v, want %s", i, got, want)
		}
	}

	// A bundle the file doesn't list, a listed bundle that wasn't built, and a bad version all fail.
	write(t, filepath.Join(dir, "unifi-mcp-1.2.3-linux-amd64.mcpb"), "linux")
	if _, err := stampServerJSON([]byte(in), "1.2.3", dir); err == nil || !strings.Contains(err.Error(), "linux-amd64") {
		t.Errorf("an unlisted bundle: %v", err)
	}
	os.Remove(filepath.Join(dir, "unifi-mcp-1.2.3-linux-amd64.mcpb"))
	os.Remove(filepath.Join(dir, "unifi-mcp-1.2.3-windows-amd64.mcpb"))
	if _, err := stampServerJSON([]byte(in), "1.2.3", dir); err == nil {
		t.Error("a missing bundle was accepted")
	}
	if _, err := stampServerJSON([]byte(in), "v1.2.3", dir); err == nil {
		t.Error("a v-prefixed version was accepted")
	}
}

// ociOnlyInBundle are bundle settings the image's package leaves out: a file
// on the user's computer isn't visible inside the container.
var ociOnlyInBundle = map[string]bool{"UNIFI_CA_FILE": true}

// server.json's image package must declare the same variables as the bundle's
// manifest, with the same required, secret and default values, so the two
// don't drift apart. The files aren't in the Docker build; CI runs this outside it.
func TestServerJSONMatchesManifest(t *testing.T) {
	m := readManifest(t)
	data, err := os.ReadFile("../../server.json")
	if os.IsNotExist(err) {
		t.Skip("server.json is not available")
	}
	if err != nil {
		t.Fatal(err)
	}
	s, ok := ojson.AsObj(ojson.MustParse(string(data)))
	if !ok {
		t.Fatal("server.json is not an object")
	}
	if s.Get("version") != m.Get("version") {
		t.Errorf("server.json version %v, want the placeholder %v", s.Get("version"), m.Get("version"))
	}
	if n := s.Get("name"); n != "io.github.mattoddie/unifi-mcp" {
		t.Errorf("name %v", n)
	}
	if d := ojson.String(s.Get("description")); len([]rune(d)) > 100 {
		t.Errorf("description is %d characters; the registry allows 100", len([]rune(d)))
	}

	oci := ojson.Find(arr(s.Get("packages")), func(p any) bool { return ojson.Get(p, "registryType") == "oci" })
	if ojson.IsUndefined(oci) {
		t.Fatal("server.json has no oci package")
	}
	declared := map[string]*ojson.Obj{}
	for _, e := range arr(ojson.Get(oci, "environmentVariables")) {
		o, _ := ojson.AsObj(e)
		declared[ojson.String(o.Get("name"))] = o
	}
	userConfig, _ := ojson.AsObj(m.Get("user_config"))
	env, _ := ojson.AsObj(ojson.Get(m, "server", "mcp_config", "env"))
	for _, name := range env.Keys() {
		value := ojson.String(env.Get(name))
		d := declared[name]
		delete(declared, name)
		if ociOnlyInBundle[name] {
			if d != nil {
				t.Errorf("%s: declared in server.json, but listed as bundle-only", name)
			}
			continue
		}
		if d == nil {
			t.Errorf("%s: set by the bundle, missing from server.json's oci package", name)
			continue
		}
		ref := userConfigRef.FindStringSubmatch(value)
		if ref == nil {
			// A fixed value, such as MCP_TRANSPORT=stdio.
			if got := ojson.String(d.Get("value")); got != value {
				t.Errorf("%s: server.json value %q, want %q", name, got, value)
			}
			continue
		}
		uc, _ := ojson.AsObj(userConfig.Get(ref[1]))
		if got, want := ojson.Truthy(d.Get("isRequired")), ojson.Truthy(uc.Get("required")); got != want {
			t.Errorf("%s: isRequired %v, want %v (user_config.%s)", name, got, want, ref[1])
		}
		if got, want := ojson.Truthy(d.Get("isSecret")), ojson.Truthy(uc.Get("sensitive")); got != want {
			t.Errorf("%s: isSecret %v, want %v (user_config.%s)", name, got, want, ref[1])
		}
		want := ""
		if def := uc.Get("default"); !ojson.IsUndefined(def) {
			want = ojson.String(def)
		}
		if got := ojson.String(ojson.Coalesce(d.Get("default"), "")); got != want {
			t.Errorf("%s: default %q, want %q (user_config.%s)", name, got, want, ref[1])
		}
		if ojson.String(d.Get("description")) == "" {
			t.Errorf("%s: no description", name)
		}
	}
	for name := range declared {
		t.Errorf("%s: declared in server.json, but the bundle doesn't set it", name)
	}
}

// The registry checks the image's label to confirm it belongs to the server.
func TestDockerfileLabel(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if os.IsNotExist(err) {
		t.Skip("Dockerfile is not available")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `io.modelcontextprotocol.server.name="io.github.mattoddie/unifi-mcp"`) {
		t.Error("the Dockerfile has no io.modelcontextprotocol.server.name label")
	}
}
