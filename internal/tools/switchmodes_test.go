package tools

import (
	"net/url"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

// A 6-port switch with overrides on ports 1, 3 and 6 and LLDP neighbours on ports 1 and 5.
const bigSwitchJSON = `{"_id":"d9","mac":"aa:bb:cc:00:00:09","name":"Rack Switch","model":"USW24","port_table":[
	{"port_idx":1,"name":"Port 1","up":true,"enable":true,"speed":1000},
	{"port_idx":2,"name":"Port 2","up":true,"enable":true,"speed":1000},
	{"port_idx":3,"name":"Port 3","up":true,"enable":true,"speed":1000},
	{"port_idx":4,"name":"Port 4","up":true,"enable":true,"speed":1000,"aggregated_by":3},
	{"port_idx":5,"name":"Port 5","up":true,"enable":true,"speed":1000},
	{"port_idx":6,"name":"Port 6","up":false,"enable":true}],
	"port_overrides":[
		{"port_idx":1,"name":"Uplink","portconf_id":"pc-all"},
		{"port_idx":3,"name":"NAS","op_mode":"aggregate","aggregate_members":[3,4],"aggregate_num_ports":2,"lag_idx":1},
		{"port_idx":4,"name":"NAS member"},
		{"port_idx":6,"name":"Camera","poe_mode":"off"}],
	"lldp_table":[
		{"local_port_idx":1,"chassis_id":"aa:bb:cc:00:00:01","chassis_descr":"Office Switch","port_id":"Port 8","port_descr":"eth7","is_wired":true},
		{"local_port_idx":5,"chassis_id":"aa:bb:cc:00:00:02","chassis_descr":"Lounge AP","port_id":"eth0","is_wired":true}]}`

func bigSwitchEnv(t *testing.T) *switchEnv {
	env := startMock(t, toolOptions(true, true))
	d := js(bigSwitchJSON)
	env.mock.
		On("GET", `^/api/s/default/stat/device/aa:bb:cc:00:00:09$`, func(any, []string, url.Values) (int, any) {
			return unifitest.OK([]any{ojson.Clone(d)})
		}).
		On("PUT", `^/api/s/default/rest/device/([^/]+)$`, func(body any, m []string, _ url.Values) (int, any) {
			return unifitest.OK([]any{ojson.New("_id", m[1]).Assign(body)})
		}).
		Seed("rest", "portconf", `[{"_id":"pc-all","name":"All","forward":"all"}]`)
	return &switchEnv{testEnv: env}
}

func overrideByPort(body any) map[float64]any {
	out := map[float64]any{}
	for _, o := range arr(get(body, "port_overrides")) {
		out[ojson.ToNumber(ojson.Get(o, "port_idx"))] = o
	}
	return out
}

func TestListSwitchPortsLLDP(t *testing.T) {
	env := bigSwitchEnv(t)
	res := env.call("unifi_list_switch_ports", `{"mac":"aa:bb:cc:00:00:09"}`)
	eq(t, "port 1 neighbour", get(res, "items", 0, "lldp_neighbors"), js(`[{"chassis_id":"aa:bb:cc:00:00:01","description":"Office Switch","port_id":"Port 8","port_description":"eth7"}]`))
	eq(t, "no neighbour", get(res, "items", 1, "lldp_neighbors"), ojson.Undefined)
	eq(t, "aggregated", get(res, "items", 3, "aggregated_by"), 3.0)
	eq(t, "op mode", get(res, "items", 2, "op_mode"), ojson.Undefined) // op_mode comes from the live port table
}

func TestSetSwitchPortMirror(t *testing.T) {
	env := bigSwitchEnv(t)
	env.begin()
	res := env.call("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":2,"mirror_port_idx":5}`)
	body := env.sent()[0].Body
	got := overrideByPort(body)
	eq(t, "mirror override", got[2], js(`{"port_idx":2,"op_mode":"mirror","mirror_port_idx":5}`))
	// Every other port's override survives the PUT unchanged.
	eq(t, "port 1 kept", got[1], js(`{"port_idx":1,"name":"Uplink","portconf_id":"pc-all"}`))
	eq(t, "port 3 kept", got[3], js(`{"port_idx":3,"name":"NAS","op_mode":"aggregate","aggregate_members":[3,4],"aggregate_num_ports":2,"lag_idx":1}`))
	eq(t, "port 4 kept", got[4], js(`{"port_idx":4,"name":"NAS member"}`))
	eq(t, "port 6 kept", got[6], js(`{"port_idx":6,"name":"Camera","poe_mode":"off"}`))
	eq(t, "result", get(res, "override", "op_mode"), "mirror")

	env.begin()
	env.call("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":3,"op_mode":"switch"}`)
	got = overrideByPort(env.sent()[0].Body)
	eq(t, "aggregation off", got[3], js(`{"port_idx":3,"name":"NAS","op_mode":"switch"}`))
	eq(t, "count", len(got), 4)

	contains(t, "self", env.callErr("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":2,"mirror_port_idx":2}`), "can't mirror to itself")
	contains(t, "missing", env.callErr("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":2,"mirror_port_idx":9}`), "valid ports")
	contains(t, "mode", env.callErr("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":2,"mirror_port_idx":5,"op_mode":"switch"}`), "needs op_mode=mirror")
}

func TestSetSwitchPortAggregate(t *testing.T) {
	env := bigSwitchEnv(t)
	env.begin()
	res := env.call("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":5,"aggregate_ports":[6,5]}`)
	got := overrideByPort(env.sent()[0].Body)
	eq(t, "lag override", got[5], js(`{"port_idx":5,"op_mode":"aggregate","aggregate_members":[5,6],"aggregate_num_ports":2,"lag_idx":2}`))
	// The new member's own override is removed; overrides of ports outside the group are kept.
	if _, ok := got[6]; ok {
		t.Error("member port 6 override should be removed")
	}
	eq(t, "port 1 kept", got[1], js(`{"port_idx":1,"name":"Uplink","portconf_id":"pc-all"}`))
	eq(t, "port 3 kept", got[3], js(`{"port_idx":3,"name":"NAS","op_mode":"aggregate","aggregate_members":[3,4],"aggregate_num_ports":2,"lag_idx":1}`))
	eq(t, "port 4 kept", got[4], js(`{"port_idx":4,"name":"NAS member"}`))
	eq(t, "removed", get(res, "removed_member_overrides"), js(`[6]`))

	// Resizing an existing group keeps its lag_idx.
	env.begin()
	env.call("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":3,"aggregate_ports":[3,4,5]}`)
	got = overrideByPort(env.sent()[0].Body)
	eq(t, "lag kept", get(got[3], "lag_idx"), 1.0)
	eq(t, "members", get(got[3], "aggregate_members"), js(`[3,4,5]`))
	if _, ok := got[4]; ok {
		t.Error("member port 4 override should be removed")
	}

	env.begin()
	contains(t, "start", env.callErr("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":2,"aggregate_ports":[1,2]}`), "must start with port_idx")
	contains(t, "gap", env.callErr("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":2,"aggregate_ports":[2,4]}`), "consecutive")
	contains(t, "both", env.callErr("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":2,"aggregate_ports":[2,3],"mirror_port_idx":5}`), "not both")
	contains(t, "range", env.callErr("unifi_set_switch_port", `{"mac":"aa:bb:cc:00:00:09","port_idx":5,"aggregate_ports":[5,6,7]}`), "valid ports")
	if len(env.sent()) != 0 {
		t.Error("errors sent requests")
	}
}
