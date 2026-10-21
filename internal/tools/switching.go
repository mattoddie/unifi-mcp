package tools

import (
	"context"
	"fmt"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// portSettingsInput are port settings shared by port profiles and per-port overrides.
func portSettingsInput() []Prop {
	return []Prop{
		P("native_network_id", Str().Opt().D("Native (untagged) network ID (see unifi_list_networks)")),
		P("tagged_vlan_mgmt", Enum("auto", "block_all", "custom").Opt().D("Tagged VLANs: auto (allow all), block_all (native VLAN only) or custom (allow all except excluded_network_ids)")),
		P("excluded_network_ids", Array(Str()).Opt().D("Network IDs NOT to tag on the port (used with tagged_vlan_mgmt=custom)")),
		P("poe_mode", Enum("auto", "pasv24", "passthrough", "off").Opt().D("PoE mode: auto (802.3af/at/bt), pasv24 (24V passive), passthrough, or off")),
		P("speed", Union(Literal("auto"), Int().Positive()).Opt().D("Link speed: 'auto' (autonegotiate) or a fixed speed in Mbps (10, 100, 1000, 2500, 10000...)")),
		P("full_duplex", Bool().Opt().D("Full duplex when a fixed speed is set (default true)")),
		P("isolation", Bool().Opt().D("Port isolation: block traffic to other isolated ports")),
		P("storm_control", Bool().Opt().D("Enable broadcast/multicast/unicast storm control")),
		P("op_mode", Enum("switch", "mirror", "aggregate").Opt().D("Operation mode: switch (normal), mirror (port mirroring) or aggregate (link aggregation)")),
	}
}

// portSettingsBody maps port settings to controller field names.
func portSettingsBody(a Args) *Obj {
	body := unifi.Defined(
		"native_networkconf_id", a.Get("native_network_id"),
		"tagged_vlan_mgmt", a.Get("tagged_vlan_mgmt"),
		"excluded_networkconf_ids", a.Get("excluded_network_ids"),
		"poe_mode", a.Get("poe_mode"),
		"isolation", a.Get("isolation"),
		"stormctrl_enabled", a.Get("storm_control"),
		"op_mode", a.Get("op_mode"),
	)
	speed := a.Get("speed")
	switch {
	case ojson.StrictEq(speed, "auto"):
		body.Set("autoneg", true)
	case !ojson.IsUndefined(speed):
		body.Set("autoneg", false).Set("speed", speed).Set("full_duplex", ojson.Coalesce(a.Get("full_duplex"), true))
	case a.Has("full_duplex"):
		body.Set("full_duplex", a.Get("full_duplex"))
	}
	return body
}

func summarizeProfile(p *Obj) *Obj {
	g := p.Get
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"forward", g("forward"),
		"native_network_id", g("native_networkconf_id"),
		"tagged_vlan_mgmt", g("tagged_vlan_mgmt"),
		"excluded_network_ids", ojson.If(ojson.HasLength(g("excluded_networkconf_ids")), g("excluded_networkconf_ids"), U),
		"poe_mode", g("poe_mode"),
		"speed", ojson.If(ojson.StrictEq(g("autoneg"), false), g("speed"), "auto"),
		"op_mode", g("op_mode"),
		"isolation", ojson.OrUndefined(g("isolation")),
		"storm_control", ojson.OrUndefined(g("stormctrl_enabled")),
		"port_security", ojson.OrUndefined(g("port_security_enabled")),
	)
}

func getSwitch(ctx context.Context, client *unifi.Client, site, m string) (*Obj, error) {
	norm, err := unifi.NormalizeMac(m)
	if err != nil {
		return nil, err
	}
	res, err := client.SiteGet(ctx, site, "stat/device/"+norm, nil)
	if err != nil {
		return nil, err
	}
	device := first(res)
	if !ojson.Truthy(device) {
		return nil, fmt.Errorf("No device with MAC %s on site %s", m, site)
	}
	d := asObj(device)
	if ports, ok := ojson.Arr(d.Get("port_table")); !ok || len(ports) == 0 {
		return nil, fmt.Errorf("Device %s has no switch ports", ojson.String(ojson.Coalesce(d.Get("name"), m)))
	}
	return d, nil
}

func findPort(device *Obj, portIdx any) (*Obj, error) {
	ports := arr(device.Get("port_table"))
	port := ojson.Find(ports, func(p any) bool { return ojson.StrictEq(ojson.Get(p, "port_idx"), portIdx) })
	if !ojson.Truthy(port) {
		valid := joinValues(ojson.Map(ports, func(p any) any { return ojson.Get(p, "port_idx") }), ", ")
		return nil, fmt.Errorf("Port %s does not exist on %s; valid ports: %s", ojson.String(portIdx), ojson.String(ojson.Coalesce(device.Get("name"), device.Get("mac"))), valid)
	}
	return asObj(port), nil
}

func registerSwitching(c *Context) {
	client := c.Client
	switchMac := P("mac", macSchema().D("MAC address of the switch"))

	// ---------------------------------------------------------------- port profiles

	c.add(Def{
		Toolset: "switching", Name: "unifi_list_port_profiles", Title: "List switch port profiles",
		Description: "List switch port profiles (port configurations) with native/tagged networks and PoE settings.",
		Input:       props(siteArg(), listing{}.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := restList(ctx, client, c.site(a), "portconf")
			if err != nil {
				return nil, err
			}
			return listing{}.result(a, list, func(p *Obj) any { return summarizeProfile(p) }, nil), nil
		},
	})

	c.add(Def{
		Toolset: "switching", Name: "unifi_save_port_profile", Title: "Create/update switch port profile",
		Description: "Create a switch port profile (omit id) or update one (pass id; only the given fields change). Ports using the profile are reprovisioned, which can briefly interrupt traffic.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("id", Str().Opt().D("Port profile ID to update; omit to create")),
			P("name", Str().MinLen(1).Opt().D("Profile name (required when creating)")),
			portSettingsInput(),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			if id == "" && !a.Truthy("name") {
				return nil, errorf("name is required when creating a port profile")
			}
			body := ojson.Spread(unifi.Defined("name", a.Get("name")), portSettingsBody(a), a.Get("extra"))
			if id != "" && body.Len() == 0 {
				return nil, errorf("Nothing to update")
			}
			saved, err := restSave(ctx, client, c.site(a), "portconf", id, body)
			if err != nil {
				return nil, err
			}
			return summarizeProfile(saved), nil
		},
	})

	c.add(Def{
		Toolset: "switching", Name: "unifi_delete_port_profile", Title: "Delete switch port profile",
		Description: "Delete a switch port profile. Ports using it fall back to the default profile.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Port profile")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			if err := restDelete(ctx, client, c.site(a), "portconf", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id")), nil
		},
	})

	// ---------------------------------------------------------------- ports

	portList := listing{Search: []string{"name", "profile_name", "media", "poe_mode"}, Filters: map[string]string{"up": "up"}}
	c.add(Def{
		Toolset: "switching", Name: "unifi_list_switch_ports", Title: "List switch ports",
		Description: "List a switch's ports with link state, speed, PoE, assigned profile, LLDP neighbours (what is plugged in: chassis ID, description and remote port), link aggregation and any per-port overrides configured on the device.",
		Input:       props(siteArg(), switchMac, P("up", Bool().Opt().D("Only ports whose link is up (true) or down (false)")), portList.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			device, err := getSwitch(ctx, client, s, a.Str("mac"))
			if err != nil {
				return nil, err
			}
			profiles, err := restList(ctx, client, s, "portconf")
			if err != nil {
				profiles = []any{}
			}
			profileNames := map[string]any{}
			for _, p := range profiles {
				profileNames[ojson.String(ojson.Get(p, "_id"))] = ojson.Get(p, "name")
			}
			overrides := map[string]*Obj{}
			for _, o := range objs(ojson.Coalesce(device.Get("port_overrides"), []any{})) {
				overrides[ojson.String(o.Get("port_idx"))] = o
			}
			neighbours := map[string][]any{}
			for _, n := range objs(ojson.Coalesce(device.Get("lldp_table"), []any{})) {
				k := ojson.String(n.Get("local_port_idx"))
				neighbours[k] = append(neighbours[k], unifi.Compact(
					"chassis_id", n.Get("chassis_id"),
					"description", n.Get("chassis_descr"),
					"port_id", n.Get("port_id"),
					"port_description", n.Get("port_descr"),
				))
			}
			ports := portList.result(a, arr(device.Get("port_table")), func(p *Obj) any {
				o := overrides[ojson.String(p.Get("port_idx"))]
				profileID := p.Get("portconf_id")
				if o != nil && !ojson.IsNullish(o.Get("portconf_id")) {
					profileID = o.Get("portconf_id")
				}
				enabled := p.Get("enable")
				if o != nil && ojson.Truthy(o.Get("port_security_enabled")) && !ojson.HasLength(o.Get("port_security_mac_address")) {
					enabled = false
				}
				opMode := p.Get("op_mode")
				var override any = U
				if o != nil {
					override = o.Without("port_idx")
				}
				var profileName any = U
				if ojson.Truthy(profileID) {
					if n, ok := profileNames[ojson.String(profileID)]; ok {
						profileName = n
					}
				}
				up := ojson.Truthy(p.Get("up"))
				return unifi.Compact(
					"idx", p.Get("port_idx"),
					"name", p.Get("name"),
					"up", p.Get("up"),
					"enabled", enabled,
					"speed_mbps", ojson.If(up, p.Get("speed"), U),
					"full_duplex", ojson.If(up, p.Get("full_duplex"), U),
					"media", p.Get("media"),
					"poe_mode", p.Get("poe_mode"),
					"poe_power_w", ojson.If(ojson.Truthy(p.Get("poe_power")), ojson.ToNumber(p.Get("poe_power")), U),
					"is_uplink", ojson.OrUndefined(p.Get("is_uplink")),
					"op_mode", ojson.If(ojson.Truthy(opMode) && !ojson.StrictEq(opMode, "switch"), opMode, U),
					"profile_id", profileID,
					"profile_name", profileName,
					"native_network_id", p.Get("native_networkconf_id"),
					"stp_state", p.Get("stp_state"),
					"aggregated_by", ojson.If(ojson.Truthy(p.Get("aggregated_by")), p.Get("aggregated_by"), U),
					"lldp_neighbors", lldpOf(neighbours, p.Get("port_idx")),
					"errors", ojson.OrUndefined(ojson.ToNumber(ojson.Coalesce(p.Get("rx_errors"), 0.0))+ojson.ToNumber(ojson.Coalesce(p.Get("tx_errors"), 0.0))),
					"override", override,
				)
			}, nil)
			return ojson.New(
				"switch", unifi.Compact("name", device.Get("name"), "mac", device.Get("mac"), "model", ojson.Coalesce(device.Get("model_name"), device.Get("model"))),
			).Assign(ports), nil
		},
	})

	c.add(Def{
		Toolset: "switching", Name: "unifi_set_switch_port", Title: "Configure switch port",
		Description: "Configure one switch port via a per-port override: name, enable/disable, port profile, native/tagged VLANs, PoE, speed, isolation, storm control, port mirroring and link aggregation. Only the given fields change; other ports' overrides are kept (except the other members of a new aggregation group, which the controller manages). Disabling uses port security with no allowed MACs (blocks all traffic), as the UniFi UI does. op_mode=switch turns mirroring or aggregation off again.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			switchMac,
			P("port_idx", Int().Positive().D("Port number (see unifi_list_switch_ports)")),
			P("name", Str().Opt().D("Port name/label")),
			P("enabled", Bool().Opt().D("false blocks all traffic on the port; true re-enables it")),
			P("profile_id", Str().Opt().D("Port profile ID to assign (see unifi_list_port_profiles)")),
			portSettingsInput(),
			P("mirror_port_idx", Int().Positive().Opt().D("Mirror this port's traffic to this port (sets op_mode=mirror). Most switches allow one mirror session.")),
			P("aggregate_ports", Array(Int().Positive()).Min(2).Max(8).Opt().D("Bond these consecutive ports, starting with port_idx, into an LACP link aggregation group (sets op_mode=aggregate). Ports must run at the same speed; the other members' own overrides are removed.")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			device, err := getSwitch(ctx, client, s, a.Str("mac"))
			if err != nil {
				return nil, err
			}
			portIdx := a.Get("port_idx")
			port, err := findPort(device, portIdx)
			if err != nil {
				return nil, err
			}
			patch := ojson.Spread(unifi.Defined("name", a.Get("name"), "portconf_id", a.Get("profile_id")), portSettingsBody(a))
			if a.Has("enabled") {
				patch.Set("port_security_enabled", !a.Truthy("enabled"))
			}
			if ojson.StrictEq(a.Get("enabled"), false) {
				patch.Set("port_security_mac_address", []any{})
			}
			overrides := arr(ojson.Clone(ojson.Coalesce(device.Get("port_overrides"), []any{})))
			members, err := portModePatch(a, device, overrides, patch)
			if err != nil {
				return nil, err
			}
			patch.Assign(a.Get("extra"))
			if patch.Len() == 0 {
				return nil, errorf("Nothing to change: pass at least one port setting")
			}

			existing := ojson.Find(overrides, func(o any) bool { return ojson.StrictEq(ojson.Get(o, "port_idx"), portIdx) })
			var override *Obj
			if o, ok := ojson.AsObj(existing); ok {
				override = o.Assign(patch)
			} else {
				override = ojson.New("port_idx", portIdx).Assign(unifi.Defined("portconf_id", port.Get("portconf_id")), patch)
				overrides = append(overrides, override)
			}
			// Drop settings left over from another operation mode.
			for _, k := range modeFields {
				switch mode := patch.Get("op_mode"); {
				case ojson.StrictEq(mode, "switch"),
					ojson.StrictEq(mode, "mirror") && k != "mirror_port_idx",
					ojson.StrictEq(mode, "aggregate") && k == "mirror_port_idx":
					override.Delete(k)
				}
			}
			// The controller manages the other members of a link aggregation group itself,
			// so their own overrides are removed. Every other port's override is kept.
			var removed []any
			if len(members) > 1 {
				others := members[1:]
				overrides = ojson.Filter(overrides, func(o any) bool {
					if ojson.Includes(others, ojson.Get(o, "port_idx")) {
						removed = append(removed, ojson.Get(o, "port_idx"))
						return false
					}
					return true
				})
			}
			ojson.SortStable(overrides, func(x, y any) float64 {
				return ojson.ToNumber(ojson.Get(x, "port_idx")) - ojson.ToNumber(ojson.Get(y, "port_idx"))
			})
			idPath, err := restPath("device", device.Get("_id"))
			if err != nil {
				return nil, err
			}
			if _, err := client.SitePut(ctx, s, idPath, ojson.New("port_overrides", overrides)); err != nil {
				return nil, err
			}
			out := ojson.New("switch", device.Get("mac"), "port_idx", portIdx, "override", override)
			if removed != nil {
				out.Set("removed_member_overrides", removed)
			}
			return out, nil
		},
	})

	c.add(Def{
		Toolset: "switching", Name: "unifi_reset_switch_port", Title: "Reset switch port override",
		Description: "Remove the per-port override from one or more switch ports so they follow the default port profile again.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			switchMac,
			P("port_idx", Array(Int().Positive()).Min(1).D("Port numbers to reset")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			device, err := getSwitch(ctx, client, s, a.Str("mac"))
			if err != nil {
				return nil, err
			}
			wanted := a.Arr("port_idx")
			for _, idx := range wanted {
				if _, err := findPort(device, idx); err != nil {
					return nil, err
				}
			}
			overrides := arr(ojson.Coalesce(device.Get("port_overrides"), []any{}))
			kept := ojson.Filter(overrides, func(o any) bool { return !ojson.Includes(wanted, ojson.Get(o, "port_idx")) })
			reset := ojson.Map(ojson.Filter(overrides, func(o any) bool { return ojson.Includes(wanted, ojson.Get(o, "port_idx")) }), func(o any) any { return ojson.Get(o, "port_idx") })
			if len(reset) > 0 {
				idPath, err := restPath("device", device.Get("_id"))
				if err != nil {
					return nil, err
				}
				if _, err := client.SitePut(ctx, s, idPath, ojson.New("port_overrides", kept)); err != nil {
					return nil, err
				}
			}
			already := ojson.Filter(wanted, func(i any) bool { return !ojson.Includes(reset, i) })
			return ojson.New("switch", device.Get("mac"), "reset", reset, "already_default", already), nil
		},
	})

	c.add(Def{
		Toolset: "switching", Name: "unifi_power_cycle_port", Title: "Power-cycle PoE port",
		Description: "Power-cycle a PoE port on a switch, rebooting the device powered by it.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			switchMac,
			P("port_idx", Int().Positive().D("Port number (see unifi_list_switch_ports)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			m, err := mac(a.Get("mac"))
			if err != nil {
				return nil, err
			}
			if _, err := client.Cmd(ctx, c.site(a), "devmgr", "power-cycle", ojson.New("mac", m, "port_idx", a.Get("port_idx"))); err != nil {
				return nil, err
			}
			return ojson.New("switch", m, "port_idx", a.Get("port_idx"), "power_cycled", true), nil
		},
	})
}

// modeFields are the port override fields that only apply to mirror and aggregate ports.
var modeFields = []string{"mirror_port_idx", "aggregate_members", "aggregate_num_ports", "lag_idx"}

// portModePatch adds port mirroring or link aggregation settings to patch and returns the
// aggregation members (sorted, port_idx first) when aggregate_ports is given.
func portModePatch(a Args, device *Obj, overrides []any, patch *Obj) ([]any, error) {
	portIdx := a.Get("port_idx")
	opMode, hasMode := a.Get("op_mode"), a.Has("op_mode")
	if a.Has("mirror_port_idx") && a.Has("aggregate_ports") {
		return nil, errorf("Pass mirror_port_idx or aggregate_ports, not both")
	}
	if a.Has("mirror_port_idx") {
		target := a.Get("mirror_port_idx")
		if hasMode && !ojson.StrictEq(opMode, "mirror") {
			return nil, errorf("mirror_port_idx needs op_mode=mirror (or leave op_mode out)")
		}
		if ojson.StrictEq(target, portIdx) {
			return nil, errorf("A port can't mirror to itself")
		}
		if _, err := findPort(device, target); err != nil {
			return nil, err
		}
		patch.Set("op_mode", "mirror").Set("mirror_port_idx", target)
		return nil, nil
	}
	if !a.Has("aggregate_ports") {
		return nil, nil
	}
	if hasMode && !ojson.StrictEq(opMode, "aggregate") {
		return nil, errorf("aggregate_ports needs op_mode=aggregate (or leave op_mode out)")
	}
	members := append([]any{}, a.Arr("aggregate_ports")...)
	ojson.SortStable(members, func(x, y any) float64 { return ojson.ToNumber(x) - ojson.ToNumber(y) })
	if !ojson.StrictEq(members[0], portIdx) {
		return nil, errorf("aggregate_ports must start with port_idx (the lowest port of the group)")
	}
	for i, m := range members {
		if i > 0 && ojson.ToNumber(m) != ojson.ToNumber(members[i-1])+1 {
			return nil, errorf("aggregate_ports must be consecutive ports, e.g. [5, 6]")
		}
		if _, err := findPort(device, m); err != nil {
			return nil, err
		}
	}
	// Keep the port's group number, or take the next free one.
	var lag any = U
	next := 1.0
	for _, o := range overrides {
		n, ok := ojson.Num(ojson.Get(o, "lag_idx"))
		if !ok {
			continue
		}
		if ojson.StrictEq(ojson.Get(o, "port_idx"), portIdx) {
			lag = n
		} else if n >= next {
			next = n + 1
		}
	}
	patch.Set("op_mode", "aggregate").
		Set("aggregate_members", members).
		Set("aggregate_num_ports", float64(len(members))).
		Set("lag_idx", ojson.Coalesce(lag, next))
	return members, nil
}

// lldpOf returns the LLDP neighbours seen on a port, or Undefined when there are none.
func lldpOf(neighbours map[string][]any, portIdx any) any {
	if list := neighbours[ojson.String(portIdx)]; len(list) > 0 {
		return list
	}
	return U
}
