package tools

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
)

var toolName = regexp.MustCompile(`\bunifi_[a-z0-9_]*[a-z0-9]\b`)

// schemaOf is a tool's input schema as plain JSON.
func schemaOf(t *testing.T, tool *mcp.Tool) map[string]any {
	t.Helper()
	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// walkProps calls f for every property in a schema, nested ones included,
// with its dot path.
func walkProps(s map[string]any, path string, f func(path string, prop map[string]any)) {
	if props, ok := s["properties"].(map[string]any); ok {
		for name, p := range props {
			prop, _ := p.(map[string]any)
			f(path+name, prop)
			walkProps(prop, path+name+".", f)
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		walkProps(items, path+"[].", f)
	}
	if values, ok := s["additionalProperties"].(map[string]any); ok {
		walkProps(values, path+"*.", f)
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		options, _ := s[key].([]any)
		for _, o := range options {
			if opt, ok := o.(map[string]any); ok {
				walkProps(opt, path, f)
			}
		}
	}
}

// Every argument of every tool, nested ones included, has a description.
func TestEveryArgumentIsDescribed(t *testing.T) {
	env := startMock(t, toolOptions(true, true))
	for name, tool := range toolMap(t, env.mcp) {
		walkProps(schemaOf(t, tool), "", func(path string, prop map[string]any) {
			if d, _ := prop["description"].(string); strings.TrimSpace(d) == "" {
				t.Errorf("%s: argument %s has no description", name, path)
			}
		})
	}
}

// A description, or an argument's, only names tools that are registered, so
// the model is never sent to a tool the configuration leaves out. Mentions of
// gated tools go in Def.Hints.
func TestDescriptionsNameOnlyRegisteredTools(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	check := func(what string, o config.Tools, args bool) {
		tools := toolMap(t, connect(t, env.client, o))
		for name, tool := range tools {
			texts := map[string]string{"description": tool.Description}
			if args {
				walkProps(schemaOf(t, tool), "", func(path string, prop map[string]any) {
					texts["argument "+path], _ = prop["description"].(string)
				})
			}
			for where, text := range texts {
				for _, named := range toolName.FindAllString(text, -1) {
					if tools[named] == nil {
						t.Errorf("%s: %s %s names %s, which isn't registered", what, name, where, named)
					}
				}
			}
		}
	}
	readOnly := toolOptions(false, false)
	readOnly.AllowSecrets, readOnly.BackupDir = false, ""
	check("read-only", readOnly, true)
	writes := toolOptions(true, false)
	writes.AllowSecrets, writes.BackupDir = false, ""
	check("writes without deletes", writes, true)
	// With a single toolset, tool descriptions still only name registered
	// tools (argument descriptions may point at unifi_list_sites and the like).
	for _, ts := range config.Toolsets {
		for _, w := range []bool{false, true} {
			o := toolOptions(w, w)
			o.Toolsets = map[string]bool{ts: true}
			check(ts+" only", o, false)
		}
	}
}

// Every hint names the tool it depends on, and that tool exists.
func TestHintsNameTheirTool(t *testing.T) {
	c := &Context{Tools: toolOptions(true, true)}
	c.addAll()
	known := map[string]bool{}
	for _, d := range c.defs {
		known[d.Name] = true
	}
	for _, d := range c.defs {
		for _, h := range d.Hints {
			if !known[h.Tool] {
				t.Errorf("%s: hint names unknown tool %s", d.Name, h.Tool)
			}
			if !strings.Contains(h.Text, h.Tool) {
				t.Errorf("%s: hint %q doesn't name %s", d.Name, h.Text, h.Tool)
			}
		}
	}
}
