package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

// toolOptions is every toolset with the given gates.
func toolOptions(writes, deletes bool) config.Tools {
	o := config.AllTools()
	o.AllowWrites, o.AllowDeletes = writes, deletes
	return o
}

type testEnv struct {
	t      *testing.T
	mock   *unifitest.Mock
	client *unifi.Client
	mcp    *mcp.ClientSession
}

// startMock starts an API-key UniFi OS mock and a client pointed at it, connected with opts.
func startMock(t *testing.T, opts config.Tools) *testEnv {
	t.Helper()
	m := unifitest.New(unifitest.Options{Mode: "unifi-os", APIKey: "key123"})
	t.Cleanup(m.Close)
	cfg, err := config.Load(config.MapEnv(map[string]string{"UNIFI_URL": m.URL, "UNIFI_API_KEY": "key123"}))
	if err != nil {
		t.Fatal(err)
	}
	client, err := unifi.New(cfg.Unifi)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return &testEnv{t: t, mock: m, client: client, mcp: connect(t, client, opts)}
}

func connect(t *testing.T, client *unifi.Client, o config.Tools) *mcp.ClientSession {
	t.Helper()
	server := NewServer(client, o, nil)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callRaw(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	if str, ok := args.(string); ok {
		args = json.RawMessage(str)
	}
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	return res.Content[0].(*mcp.TextContent).Text
}

// call runs a tool that must succeed and returns its parsed JSON result. args may be JSON text.
func (e *testEnv) call(name string, args any) any {
	e.t.Helper()
	return callWith(e.t, e.mcp, name, args)
}

func callWith(t *testing.T, s *mcp.ClientSession, name string, args any) any {
	t.Helper()
	res := callRaw(t, s, name, args)
	if res.IsError {
		t.Fatalf("%s failed: %s", name, text(res))
	}
	v, err := ojson.Parse([]byte(text(res)))
	if err != nil {
		t.Fatalf("%s: %v in %s", name, err, text(res))
	}
	return v
}

// callErr runs a tool that must fail and returns its error text.
func (e *testEnv) callErr(name string, args any) string {
	e.t.Helper()
	res := callRaw(e.t, e.mcp, name, args)
	if !res.IsError {
		e.t.Fatalf("%s: expected a tool error, got %s", name, text(res))
	}
	return text(res)
}

func toolMap(t *testing.T, s *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	out := map[string]*mcp.Tool{}
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out[tool.Name] = tool
	}
	return out
}

// get walks a parsed JSON value: get(v, "items", 0, "name").
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			v = ojson.Get(v, k)
		case int:
			a, _ := ojson.Arr(v)
			if k < 0 {
				k += len(a)
			}
			if k < 0 || k >= len(a) {
				return ojson.Undefined
			}
			v = a[k]
		}
	}
	return v
}

// js parses JSON text for comparisons.
func js(s string) any { return ojson.MustParse(s) }

// same compares JSON values, ignoring key order (like assert.deepEqual).
func same(a, b any) bool {
	return reflect.DeepEqual(plain(a), plain(b))
}

func plain(v any) any {
	s, ok := ojson.Stringify(v)
	if !ok {
		return nil
	}
	var out any
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !same(got, want) {
		gs, _ := ojson.Stringify(got)
		ws, _ := ojson.Stringify(want)
		t.Errorf("%s:\n got  %s\n want %s", what, gs, ws)
	}
}

func contains(t *testing.T, what, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%s: %q does not contain %q", what, s, sub)
	}
}

// lastBody is the body of the last request with this method.
func lastBody(m *unifitest.Mock, method string) any {
	reqs := m.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == method {
			return reqs[i].Body
		}
	}
	return nil
}

// lastTo is the last request to a path prefix.
func lastTo(m *unifitest.Mock, prefix string) unifitest.Request {
	reqs := m.RequestsTo(prefix)
	if len(reqs) == 0 {
		return unifitest.Request{}
	}
	return reqs[len(reqs)-1]
}

// firstTo is the first request to a path prefix with this method.
func firstTo(m *unifitest.Mock, prefix, method string) unifitest.Request {
	for _, r := range m.RequestsTo(prefix) {
		if r.Method == method {
			return r
		}
	}
	return unifitest.Request{}
}
