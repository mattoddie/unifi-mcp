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
		Description: "List a switch's ports with link state, speed, PoE, assigned profile and any per-port overrides configured on the device.",
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
		Description: "Configure one switch port via a per-port override: name, enable/disable, port profile, native/tagged VLANs, PoE, speed, isolation, storm control. Only the given fields change; other ports are untouched. Disabling uses port security with no allowed MACs (blocks all traffic), as the UniFi UI does.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			switchMac,
			P("port_idx", Int().Positive().D("Port number (see unifi_list_switch_ports)")),
			P("name", Str().Opt().D("Port name/label")),
			P("enabled", Bool().Opt().D("false blocks all traffic on the port; true re-enables it")),
			P("profile_id", Str().Opt().D("Port profile ID to assign (see unifi_list_port_profiles)")),
			portSettingsInput(),
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
			patch.Assign(a.Get("extra"))
			if patch.Len() == 0 {
				return nil, errorf("Nothing to change: pass at least one port setting")
			}

			overrides := arr(ojson.Clone(ojson.Coalesce(device.Get("port_overrides"), []any{})))
			existing := ojson.Find(overrides, func(o any) bool { return ojson.StrictEq(ojson.Get(o, "port_idx"), portIdx) })
			var override *Obj
			if o, ok := ojson.AsObj(existing); ok {
				override = o.Assign(patch)
			} else {
				override = ojson.New("port_idx", portIdx).Assign(unifi.Defined("portconf_id", port.Get("portconf_id")), patch)
				overrides = append(overrides, override)
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
			return ojson.New("switch", device.Get("mac"), "port_idx", portIdx, "override", override), nil
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
