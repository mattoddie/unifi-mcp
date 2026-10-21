package tools

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

func args(s string) Args {
	o, _ := ojson.AsObj(js(s))
	return Args{o}
}

// listItems are controller objects; summarize renames some fields, as real summaries do.
var listItems = `[
	{"_id":"1","name":"Office Switch","mac":"aa:bb:cc:00:00:01","ip":"10.0.0.2","state":1,"uplink":{"name":"Gateway","port":1},"radios":[{"radio":"ng","channel":6},{"radio":"na","channel":36}]},
	{"_id":"2","name":"Lounge AP","mac":"aa:bb:cc:00:00:02","ip":"10.0.0.3","state":0,"hostname":"lounge-ap","radios":[{"radio":"na","channel":44}]},
	{"_id":"3","name":"Garage AP","mac":"aa:bb:cc:00:00:03","state":0,"wired":false}
]`

func summarizeTest(o *Obj) any {
	return ojson.New("id", o.Get("_id"), "name", o.Get("name"), "mac", o.Get("mac"), "ip", o.Get("ip"),
		"state", ojson.If(ojson.StrictEq(o.Get("state"), 1.0), "connected", "disconnected"),
		"uplink", o.Get("uplink"), "radios", o.Get("radios"), "wired", o.Get("wired"))
}

func runList(l listing, a string) any {
	return l.result(args(a), ojson.MustParse(listItems).([]any), summarizeTest, asIs)
}

func itemNames(v any) []string {
	var out []string
	for _, it := range arr(ojson.Get(v, "items")) {
		out = append(out, ojson.String(ojson.Get(it, "name")))
	}
	return out
}

func TestListSearch(t *testing.T) {
	l := listing{Search: []string{"name", "hostname", "ip", "radios.channel"}}
	eq(t, "case-insensitive", itemNames(runList(l, `{"search":"ap"}`)), []string{"Lounge AP", "Garage AP"})
	eq(t, "controller-only field", itemNames(runList(l, `{"search":"LOUNGE-AP"}`)), []string{"Lounge AP"})
	eq(t, "number in an array", itemNames(runList(l, `{"search":"44"}`)), []string{"Lounge AP"})
	eq(t, "unlisted field", itemNames(runList(l, `{"search":"aa:bb"}`)), []string(nil))
	// With no search fields, every top-level string of the summary is searched.
	eq(t, "default fields", itemNames(runList(listing{}, `{"search":"00:03"}`)), []string{"Garage AP"})
	eq(t, "counts", runList(l, `{"search":"ap"}`).(*Obj).Without("items"), js(`{"total":2,"returned":2}`))
}

func TestListLimit(t *testing.T) {
	l := listing{Limit: 2}
	res := runList(l, `{}`)
	eq(t, "default limit", res.(*Obj).Without("items"), js(`{"total":3,"returned":2}`))
	eq(t, "first items", itemNames(res), []string{"Office Switch", "Lounge AP"})
	eq(t, "explicit limit", runList(l, `{"limit":1}`).(*Obj).Without("items"), js(`{"total":3,"returned":1}`))
	// Without a default the list is unlimited and has no limit argument.
	eq(t, "unlimited", runList(listing{}, `{}`).(*Obj).Without("items"), js(`{"total":3,"returned":3}`))
	if n := len(listing{}.args()); n != 2 {
		t.Errorf("args without limit: %d", n)
	}
	if n := len(l.args()); n != 3 {
		t.Errorf("args with limit: %d", n)
	}
	// Endpoints that apply the limit themselves are asked for more when results are narrowed afterwards.
	eq(t, "fetch", l.fetch(args(`{"limit":5}`)), 5.0)
	eq(t, "fetch with search", l.fetch(args(`{"limit":5,"search":"x"}`)), float64(filterFetch))
}

func TestListFields(t *testing.T) {
	res := runList(listing{}, `{"fields":["name","uplink.name","radios.channel"," "]}`)
	eq(t, "items", get(res, "items"), js(`[
		{"name":"Office Switch","uplink":{"name":"Gateway"},"radios":[{"channel":6},{"channel":36}]},
		{"name":"Lounge AP","radios":[{"channel":44}]},
		{"name":"Garage AP"}]`))
	eq(t, "no hint", get(res, "hint"), ojson.Undefined)

	// Fields apply to raw objects too.
	raw := runList(listing{}, `{"raw":true,"fields":["_id","hostname"]}`)
	eq(t, "raw", get(raw, "items"), js(`[{"_id":"1"},{"_id":"2","hostname":"lounge-ap"},{"_id":"3"}]`))

	// A whole object and one of its fields: the whole object wins.
	eq(t, "overlap", get(runList(listing{}, `{"fields":["uplink.port","uplink"],"limit":1}`), "items", 0), js(`{"uplink":{"name":"Gateway","port":1}}`))

	miss := runList(listing{}, `{"fields":["hostname","uplink.speed"]}`)
	eq(t, "empty items", get(miss, "items"), js(`[{},{},{}]`))
	eq(t, "hint", get(miss, "hint"), "None of the requested fields (hostname, uplink.speed) exist. Available fields: id, name, mac, ip, state, uplink, radios.")
}

func TestListFilters(t *testing.T) {
	l := listing{Filters: map[string]string{"state": "state", "wired": "wired", "ap": "uplink.name"}}
	eq(t, "string, any case", itemNames(runList(l, `{"state":"Disconnected"}`)), []string{"Lounge AP", "Garage AP"})
	eq(t, "nested", itemNames(runList(l, `{"ap":"gateway"}`)), []string{"Office Switch"})
	// Summaries leave false flags out, so false also matches a missing field.
	eq(t, "false", itemNames(runList(l, `{"wired":false}`)), []string{"Office Switch", "Lounge AP", "Garage AP"})
	eq(t, "true", itemNames(runList(l, `{"wired":true}`)), []string(nil))
	eq(t, "combined with search", itemNames(runList(listing{Search: []string{"name"}, Filters: l.Filters}, `{"state":"disconnected","search":"garage"}`)), []string{"Garage AP"})
}

func TestListDevicesNarrowed(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	res := env.call("unifi_list_devices", `{"type":"access_point","state":"disconnected","fields":["name","ip","state"]}`)
	eq(t, "offline APs", res, js(`{"total":1,"returned":1,"items":[{"name":"Lounge AP","state":"disconnected"}]}`))
	res = env.call("unifi_list_devices", `{"fields":["nmae"]}`)
	contains(t, "hint", ojson.String(get(res, "hint")), "Available fields: id, name, mac")
	res = env.call("unifi_list_devices", `{"limit":1}`)
	eq(t, "limit", res.(*Obj).Without("items"), js(`{"total":2,"returned":1}`))
}

func TestListClientsFilters(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	eq(t, "network", env.call("unifi_list_clients", `{"network":"nowhere"}`).(*Obj).Without("items"), js(`{"total":0,"returned":0}`))
	res := env.call("unifi_list_clients", `{"search":"10.0.0","fields":["name","connection"]}`)
	eq(t, "fields", get(res, "items"), js(`[{"name":"laptop","connection":"wireless"},{"name":"NAS","connection":"wired"}]`))
}

func TestListSwitchPortsUp(t *testing.T) {
	env := newSwitchEnv(t)
	all := env.call("unifi_list_switch_ports", `{"mac":"aa:bb:cc:00:00:01","fields":["idx","up"]}`)
	up := env.call("unifi_list_switch_ports", `{"mac":"aa:bb:cc:00:00:01","up":true,"fields":["idx"]}`)
	down := env.call("unifi_list_switch_ports", `{"mac":"aa:bb:cc:00:00:01","up":false,"fields":["idx"]}`)
	eq(t, "switch kept", get(up, "switch", "name"), get(all, "switch", "name"))
	n := func(v any) float64 { return ojson.ToNumber(get(v, "total")) }
	if n(up)+n(down) != n(all) || n(up) == 0 {
		t.Errorf("up %v + down %v != all %v", n(up), n(down), n(all))
	}
}

func TestListEventsSearchFetchesMore(t *testing.T) {
	env := startMock(t, toolOptions(false, false))
	env.mock.On("POST", `^/api/s/default/stat/event$`, func(any, []string, url.Values) (int, any) {
		return unifitest.OK(js(`[{"time":1700000000,"key":"EVT_AP_Lost_Contact","msg":"AP lost"},{"time":1699999999,"key":"EVT_SW_Connected","msg":"switch up"}]`))
	})
	res := env.call("unifi_list_events", `{"search":"lost_contact","limit":5}`)
	eq(t, "match", res.(*Obj).Without("items"), js(`{"total":1,"returned":1}`))
	eq(t, "fetched", get(env.mock.Last().Body, "_limit"), float64(filterFetch))
}

func TestListToolsShareArguments(t *testing.T) {
	env := startMock(t, toolOptions(true, true))
	checked := 0
	defer func() {
		if checked < 30 {
			t.Errorf("only %d list tools checked", checked)
		}
	}()
	for name, tool := range toolMap(t, env.mcp) {
		if !strings.HasPrefix(name, "unifi_list_") {
			continue
		}
		checked++
		props := ojson.Get(js(mustJSON(tool.InputSchema)), "properties")
		for _, arg := range []string{"search", "fields"} {
			if ojson.IsUndefined(ojson.Get(props, arg)) {
				t.Errorf("%s has no %s argument", name, arg)
			}
		}
	}
}
