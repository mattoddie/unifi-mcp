package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

var deviceTypeCodes = map[string][]string{
	"access_point": {"uap"},
	"switch":       {"usw"},
	"gateway":      {"ugw", "udm", "uxg"},
	"other":        {},
}

func getDevice(ctx context.Context, client *unifi.Client, site, m string) (*Obj, error) {
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
	return asObj(device), nil
}

func summarizeRadioConfig(r *Obj) *Obj {
	return unifi.Compact(
		"radio", r.Get("radio"),
		"channel", r.Get("channel"),
		"channel_width", r.Get("ht"),
		"tx_power_mode", r.Get("tx_power_mode"),
		"tx_power", ojson.If(ojson.StrictEq(r.Get("tx_power_mode"), "custom"), r.Get("tx_power"), U),
		"min_rssi", ojson.If(ojson.Truthy(r.Get("min_rssi_enabled")), r.Get("min_rssi"), U),
	)
}

func summarizeAPGroup(g *Obj) *Obj {
	var count any = U
	if a, ok := ojson.Arr(g.Get("device_macs")); ok {
		count = len(a)
	}
	return unifi.Compact(
		"id", g.Get("_id"),
		"name", g.Get("name"),
		"device_macs", g.Get("device_macs"),
		"device_count", count,
		"default", ojson.OrUndefined(ojson.Or(ojson.StrictEq(g.Get("attr_hidden_id"), "default"), g.Get("attr_no_delete"))),
	)
}

var siteIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{24}$`)

// resolveSiteID resolves a site name ("default"), description or _id to the site _id required by move-device.
func resolveSiteID(ctx context.Context, client *unifi.Client, ref string) (any, error) {
	if siteIDPattern.MatchString(ref) {
		return ref, nil
	}
	res, err := client.Request(ctx, "GET", "/api/self/sites", nil)
	if err != nil {
		return nil, err
	}
	sites := arr(res)
	match := ojson.Find(sites, func(s any) bool {
		return ojson.StrictEq(ojson.Get(s, "name"), ref) || ojson.StrictEq(ojson.Get(s, "desc"), ref)
	})
	if ojson.IsUndefined(match) {
		names := make([]string, len(sites))
		for i, s := range sites {
			names[i] = fmt.Sprintf("%s (%s)", ojson.String(ojson.Get(s, "name")), ojson.String(ojson.Get(s, "desc")))
		}
		return nil, fmt.Errorf(`No site named "%s"; available: %s`, ref, strings.Join(names, ", "))
	}
	return ojson.Get(match, "_id"), nil
}

func registerDevices(c *Context) {
	client := c.Client
	rawDesc := "Return the full unprocessed objects from the controller (large)"
	apMac := P("mac", macSchema().D("MAC address of the access point"))

	// ---------------------------------------------------------------- read

	c.add(Def{
		Toolset: "devices", Name: "unifi_list_devices", Title: "List devices",
		Description: "List UniFi network devices (gateways, switches, access points...) with state, model, firmware, uptime, load and client counts.",
		Input: props(
			siteArg(),
			P("type", Enum("access_point", "switch", "gateway", "other").Opt().D("Only devices of this type")),
			searchArg(),
			P("raw", Bool().Opt().D(rawDesc)),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "stat/device", nil)
			if err != nil {
				return nil, err
			}
			devices := arr(res)
			if t := a.Str("type"); t != "" {
				var known []string
				for _, k := range []string{"access_point", "switch", "gateway", "other"} {
					known = append(known, deviceTypeCodes[k]...)
				}
				devices = ojson.Filter(devices, func(d any) bool {
					dt := ojson.Get(d, "type")
					if t == "other" {
						return !ojson.Includes(ojson.Strings(known), dt)
					}
					return ojson.Includes(ojson.Strings(deviceTypeCodes[t]), dt)
				})
			}
			out := filterAndLimit(devices, a.Str("search"), nil, func(d *Obj) []any {
				return []any{d.Get("name"), d.Get("hostname"), d.Get("mac"), d.Get("ip"), d.Get("model"), d.Get("model_name")}
			})
			items := arr(out.Get("items"))
			if a.Truthy("raw") {
				out.Set("items", unifi.RedactSecrets(items))
			} else {
				out.Set("items", mapObjs(items, func(d *Obj) any { return unifi.SummarizeDevice(d) }))
			}
			return out, nil
		},
	})

	c.add(Def{
		Toolset: "devices", Name: "unifi_get_device", Title: "Get device details",
		Description: "Get detailed information about one device by MAC: summary, switch ports, radio config and stats, uplink, LED and system stats.",
		Input:       props(siteArg(), macArg(), P("raw", Bool().Opt().D(rawDesc))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			device, err := getDevice(ctx, client, c.site(a), a.Str("mac"))
			if err != nil {
				return nil, err
			}
			if a.Truthy("raw") {
				return unifi.RedactSecrets(device), nil
			}
			g := device.Get
			mapOpt := func(v any, f func(*Obj) any) any {
				if _, ok := ojson.Arr(v); !ok {
					return U
				}
				return mapObjs(v, f)
			}
			wan := func(v any) any {
				if !ojson.Truthy(v) {
					return U
				}
				return unifi.Compact("ip", ojson.Get(v, "ip"), "up", ojson.Get(v, "up"), "speed", ojson.Get(v, "speed"))
			}
			var loadAvg any = U
			if s := g("sys_stats"); ojson.Truthy(s) {
				loadAvg = []any{
					ojson.ToNumber(ojson.Get(s, "loadavg_1")),
					ojson.ToNumber(ojson.Get(s, "loadavg_5")),
					ojson.ToNumber(ojson.Get(s, "loadavg_15")),
				}
			}
			out := ojson.Spread(unifi.SummarizeDevice(device))
			for _, kv := range [][2]any{
				{"serial", g("serial")},
				{"disabled", ojson.OrUndefined(g("disabled"))},
				{"config_network", g("config_network")},
				{"ports", mapOpt(g("port_table"), func(p *Obj) any { return unifi.SummarizePort(p) })},
				{"port_overrides", ojson.If(ojson.HasLength(g("port_overrides")), g("port_overrides"), U)},
				{"radio_config", mapOpt(g("radio_table"), func(r *Obj) any { return summarizeRadioConfig(r) })},
				{"radios", mapOpt(g("radio_table_stats"), func(r *Obj) any { return unifi.SummarizeRadio(r) })},
				{"outlets", mapOpt(g("outlet_table"), func(o *Obj) any {
					return unifi.Compact("index", o.Get("index"), "name", o.Get("name"), "relay_state", o.Get("relay_state"), "cycle_enabled", o.Get("cycle_enabled"), "power_w", o.Get("outlet_power"))
				})},
				{"wan1", wan(g("wan1"))},
				{"wan2", wan(g("wan2"))},
				{"system_stats", g("system-stats")},
				{"load_avg", loadAvg},
				{"total_poe_w", g("total_used_power")},
				{"led_override", g("led_override")},
				{"led_color", g("led_override_color")},
				{"led_brightness", g("led_override_color_brightness")},
				{"locating", ojson.OrUndefined(g("locating"))},
			} {
				out.Set(kv[0].(string), kv[1])
			}
			return unifi.CompactObj(out), nil
		},
	})

	c.add(Def{
		Toolset: "devices", Name: "unifi_get_firmware_status", Title: "Get firmware status",
		Description: "Show each device's current firmware and whether an upgrade is available. Optionally ask the controller to refresh its firmware cache first (requires writes enabled), and/or list the firmware images it knows about.",
		Input: props(
			siteArg(),
			P("refresh", Bool().Opt().D("Ask the controller to check for new device firmware first (cmd/productinfo; requires UNIFI_ALLOW_WRITES=true)")),
			P("include_available", Bool().Opt().D("Also list firmware images available to the controller (cmd/firmware list-available)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			if a.Truthy("refresh") {
				if !c.AllowWrites {
					return nil, errorf("refresh requires UNIFI_ALLOW_WRITES=true")
				}
				if _, err := client.SitePost(ctx, s, "cmd/productinfo", ojson.New("cmd", "check-firmware-update")); err != nil {
					return nil, err
				}
			}
			res, err := client.SiteGet(ctx, s, "stat/device", nil)
			if err != nil {
				return nil, err
			}
			devices := arr(res)
			var available any = U
			if a.Truthy("include_available") {
				available, err = client.Cmd(ctx, s, "firmware", "list-available", nil)
				if err != nil {
					available = ojson.New("error", err.Error())
				}
			}
			upgradable := 0
			for _, d := range devices {
				if ojson.Truthy(ojson.Get(d, "upgradable")) {
					upgradable++
				}
			}
			return unifi.Compact(
				"upgradable_count", upgradable,
				"devices", mapObjs(devices, func(d *Obj) any {
					up := ojson.Truthy(d.Get("upgradable"))
					return unifi.Compact(
						"name", ojson.Coalesce(d.Get("name"), d.Get("hostname")),
						"mac", d.Get("mac"),
						"model", ojson.Coalesce(d.Get("model_name"), d.Get("model")),
						"firmware", d.Get("version"),
						"upgradable", up,
						"upgrade_to_firmware", ojson.If(up, d.Get("upgrade_to_firmware"), U),
						"upgrading", ojson.OrUndefined(ojson.StrictEq(d.Get("state"), 4.0)),
					)
				}),
				"available", available,
			), nil
		},
	})

	c.add(Def{
		Toolset: "devices", Name: "unifi_get_rf_scan", Title: "Get RF scan results",
		Description: "Get the state and results of an access point's RF/spectrum scan (channel utilization and interference per channel). Start one with unifi_run_rf_scan.",
		Input:       props(siteArg(), apMac),
		Handler: func(ctx context.Context, a Args) (any, error) {
			m, err := mac(a.Get("mac"))
			if err != nil {
				return nil, err
			}
			res, err := client.SiteGet(ctx, c.site(a), "stat/spectrum-scan/"+m, nil)
			if err != nil {
				return nil, err
			}
			r := first(res)
			if !ojson.Truthy(r) {
				return ojson.New("scanning", false, "results", []any{}), nil
			}
			obj := asObj(r)
			table := obj.Get("spectrum_table")
			rest := obj.Without("spectrum_table", "spectrum_scanning", "spectrum_scan_last_ts")
			var results any = U
			if _, ok := ojson.Arr(table); ok {
				results = mapObjs(table, func(e *Obj) any { return unifi.CompactObj(e) })
			}
			out := ojson.New("scanning", obj.Get("spectrum_scanning"), "last_scan", unifi.IsoTime(obj.Get("spectrum_scan_last_ts")), "results", results)
			if ojson.IsUndefined(table) {
				out.Assign(rest)
			}
			return unifi.CompactObj(out), nil
		},
	})

	c.add(Def{
		Toolset: "devices", Name: "unifi_list_ap_groups", Title: "List AP groups",
		Description: "List access point groups (used to choose which APs broadcast each WiFi network). Network 7.x+ (v2 API).",
		Input:       props(siteArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			groups, err := v2List(ctx, client, c.site(a), "apgroups")
			if err != nil {
				return nil, err
			}
			return mapObjs(groups, func(g *Obj) any { return summarizeAPGroup(g) }), nil
		},
	})

	// ---------------------------------------------------------------- write

	c.add(Def{
		Toolset: "devices", Name: "unifi_update_device", Title: "Update device settings",
		Description: "Change a device's settings: name, LED (on/off/site default, plus colour/brightness on LED-ring devices), enable/disable (disabling turns off its LED and radios; mainly supported on APs) and management IP (DHCP or static). Only the given fields change.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			macArg(),
			P("name", Str().MinLen(1).Opt().D("New display name")),
			P("led", Enum("on", "off", "default").Opt().D("LED override; default follows the site-wide LED setting")),
			P("led_color", Str().Pattern(`^#(?:[0-9a-fA-F]{3}){1,2}$`).Opt().D("LED colour as hex, e.g. #0000FF (LED-ring devices only)")),
			P("led_brightness", Int().Min(0).Max(100).Opt().D("LED brightness 0-100 (LED-ring devices only)")),
			P("disabled", Bool().Opt().D("true disables the device, false re-enables it")),
			P("ip_config", Object(
				P("type", Enum("dhcp", "static")),
				P("ip", Str().Opt()),
				P("netmask", Str().Opt()),
				P("gateway", Str().Opt()),
				P("dns1", Str().Opt()),
				P("dns2", Str().Opt()),
				P("dns_suffix", Str().Opt()),
			).Opt().D("Management IP configuration; static requires ip, netmask and gateway. A wrong static IP can make the device unreachable.")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			device, err := getDevice(ctx, client, s, a.Str("mac"))
			if err != nil {
				return nil, err
			}
			body := unifi.Defined(
				"name", a.Get("name"),
				"led_override", a.Get("led"),
				"led_override_color", a.Get("led_color"),
				"led_override_color_brightness", a.Get("led_brightness"),
				"disabled", a.Get("disabled"),
			)
			if ip := a.Obj("ip_config"); ip != nil {
				typ := ip.Get("type")
				rest := ip.Without("type")
				if ojson.StrictEq(typ, "static") && !(ojson.Truthy(rest.Get("ip")) && ojson.Truthy(rest.Get("netmask")) && ojson.Truthy(rest.Get("gateway"))) {
					return nil, errorf("A static ip_config needs ip, netmask and gateway")
				}
				if ojson.StrictEq(typ, "dhcp") {
					body.Set("config_network", ojson.New("type", "dhcp"))
				} else {
					body.Set("config_network", ojson.New("type", "static").Assign(rest))
				}
			}
			body.Assign(a.Get("extra"))
			if body.Len() == 0 {
				return nil, errorf("Nothing to update")
			}
			idPath, err := restPath("device", device.Get("_id"))
			if err != nil {
				return nil, err
			}
			if _, err := client.SitePut(ctx, s, idPath, body); err != nil {
				return nil, err
			}
			return ojson.New("mac", device.Get("mac"), "updated", body), nil
		},
	})

	c.add(Def{
		Toolset: "devices", Name: "unifi_set_radio", Title: "Configure AP radio",
		Description: "Change one radio on an access point: channel, channel width, transmit power and minimum RSSI. Only the given fields change. Clients on that radio will briefly disconnect.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			apMac,
			P("radio", Enum("ng", "na", "6e", "ad").D("Radio: ng = 2.4 GHz, na = 5 GHz, 6e = 6 GHz, ad = 60 GHz")),
			P("channel", Union(Literal("auto"), Int().Positive()).Opt().D("'auto' or a channel number")),
			P("channel_width", Union(Literal(20.0), Literal(40.0), Literal(80.0), Literal(160.0), Literal(240.0), Literal(320.0)).Opt().D("Channel width in MHz")),
			P("tx_power_mode", Enum("auto", "high", "medium", "low", "custom").Opt().D("Transmit power mode")),
			P("tx_power", Int().Opt().D("Transmit power in dBm (sets tx_power_mode=custom)")),
			P("min_rssi", Int().Max(-40).Null().Opt().D("Minimum RSSI in dBm (e.g. -75) below which clients are kicked; null disables it")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			device, err := getDevice(ctx, client, s, a.Str("mac"))
			if err != nil {
				return nil, err
			}
			table := arr(ojson.Clone(ojson.Coalesce(device.Get("radio_table"), []any{})))
			radio := ojson.Find(table, func(r any) bool { return ojson.StrictEq(ojson.Get(r, "radio"), a.Get("radio")) })
			if ojson.IsUndefined(radio) {
				radios := joinValues(ojson.Map(table, func(r any) any { return ojson.Get(r, "radio") }), ", ")
				if radios == "" {
					radios = "none"
				}
				return nil, fmt.Errorf("%s has no %s radio; radios: %s", ojson.String(ojson.Coalesce(device.Get("name"), device.Get("mac"))), a.Str("radio"), radios)
			}
			txMode := a.Get("tx_power_mode")
			if a.Has("tx_power") {
				txMode = "custom"
			}
			patch := unifi.Defined("channel", a.Get("channel"), "ht", a.Get("channel_width"), "tx_power_mode", txMode, "tx_power", a.Get("tx_power"))
			if a.Has("min_rssi") {
				if a.Get("min_rssi") == nil {
					patch.Set("min_rssi_enabled", false)
				} else {
					patch.Set("min_rssi_enabled", true).Set("min_rssi", a.Get("min_rssi"))
				}
			}
			patch.Assign(a.Get("extra"))
			if patch.Len() == 0 {
				return nil, errorf("Nothing to change")
			}
			r := asObj(radio)
			r.Assign(patch)
			idPath, err := restPath("device", device.Get("_id"))
			if err != nil {
				return nil, err
			}
			if _, err := client.SitePut(ctx, s, idPath, ojson.New("radio_table", table)); err != nil {
				return nil, err
			}
			return ojson.New("mac", device.Get("mac"), "radio", summarizeRadioConfig(r)), nil
		},
	})

	c.add(Def{
		Toolset: "devices", Name: "unifi_set_outlet", Title: "Control power outlet",
		Description: "Switch an outlet on a UniFi SmartPower device (USP plug/strip/PDU) on or off, toggle its auto power-cycle (modem reset), or rename it. Only the given fields change.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("mac", macSchema().D("MAC address of the SmartPower device")),
			P("index", Int().Positive().D("Outlet index (see unifi_get_device outlets[].index)")),
			P("on", Bool().Opt().D("Relay state: true = power on, false = power off")),
			P("cycle_enabled", Bool().Opt().D("Automatically power-cycle the outlet when the internet connection drops")),
			P("name", Str().Opt().D("Outlet name")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			device, err := getDevice(ctx, client, s, a.Str("mac"))
			if err != nil {
				return nil, err
			}
			index := a.Get("index")
			outlet := ojson.Find(arr(device.Get("outlet_table")), func(o any) bool { return ojson.StrictEq(ojson.Get(o, "index"), index) })
			if !ojson.Truthy(outlet) {
				return nil, fmt.Errorf("%s has no outlet %s", ojson.String(ojson.Coalesce(device.Get("name"), device.Get("mac"))), ojson.String(index))
			}
			patch := unifi.Defined("relay_state", a.Get("on"), "cycle_enabled", a.Get("cycle_enabled"), "name", a.Get("name"))
			if patch.Len() == 0 {
				return nil, errorf("Nothing to change")
			}
			overrides := arr(ojson.Clone(ojson.Coalesce(device.Get("outlet_overrides"), []any{})))
			override := ojson.Find(overrides, func(o any) bool { return ojson.StrictEq(ojson.Get(o, "index"), index) })
			if o, ok := ojson.AsObj(override); ok {
				o.Assign(patch)
			} else {
				override = ojson.New("index", index).Assign(
					unifi.Defined("name", ojson.Get(outlet, "name"), "relay_state", ojson.Get(outlet, "relay_state"), "cycle_enabled", ojson.Get(outlet, "cycle_enabled")),
					patch,
				)
				overrides = append(overrides, override)
			}
			idPath, err := restPath("device", device.Get("_id"))
			if err != nil {
				return nil, err
			}
			if _, err := client.SitePut(ctx, s, idPath, ojson.New("outlet_overrides", overrides)); err != nil {
				return nil, err
			}
			return ojson.New("mac", device.Get("mac"), "outlet", override), nil
		},
	})

	devCmd := func(name, title, desc string, gate Gate, destructive bool, extra []Prop, run func(ctx context.Context, a Args, mac string) (any, error)) {
		d := Def{Toolset: "devices", Name: name, Title: title, Description: desc, Gate: gate, Input: props(siteArg(), extra)}
		if destructive {
			d.Destructive = boolPtr(true)
		}
		d.Handler = func(ctx context.Context, a Args) (any, error) {
			m, err := mac(a.Get("mac"))
			if err != nil {
				return nil, err
			}
			return run(ctx, a, m)
		}
		c.add(d)
	}

	devCmd("unifi_restart_device", "Restart device",
		"Restart a UniFi device. reboot_type=hard also power-cycles PoE ports on switches (connected PoE devices will reboot too).",
		Write, true,
		[]Prop{macArg(), P("reboot_type", Enum("soft", "hard").Opt().D("soft (default) or hard"))},
		func(ctx context.Context, a Args, m string) (any, error) {
			rebootType := ojson.Coalesce(a.Get("reboot_type"), "soft")
			if _, err := client.Cmd(ctx, c.site(a), "devmgr", "restart", ojson.New("mac", m, "reboot_type", rebootType)); err != nil {
				return nil, err
			}
			return ojson.New("restarting", m), nil
		})

	devCmd("unifi_locate_device", "Locate device (flash LED)",
		"Start or stop flashing a device's LED so it can be found physically.",
		Write, false,
		[]Prop{macArg(), P("enabled", Bool().D("true to start flashing, false to stop"))},
		func(ctx context.Context, a Args, m string) (any, error) {
			cmd := "unset-locate"
			if a.Truthy("enabled") {
				cmd = "set-locate"
			}
			if _, err := client.Cmd(ctx, c.site(a), "devmgr", cmd, ojson.New("mac", m)); err != nil {
				return nil, err
			}
			return ojson.New("mac", m, "locating", a.Get("enabled")), nil
		})

	devCmd("unifi_upgrade_device", "Upgrade device firmware",
		"Upgrade a device to the latest firmware known to the controller, or to a specific firmware image URL. The device is offline while it upgrades and reboots. A wrong custom image can brick the device.",
		Write, true,
		[]Prop{macArg(), P("firmware_url", Str().URL().Opt().D("Upgrade to this firmware image instead of the latest (advanced)"))},
		func(ctx context.Context, a Args, m string) (any, error) {
			if fw := a.Str("firmware_url"); fw != "" {
				if _, err := client.SitePost(ctx, c.site(a), "cmd/devmgr/upgrade-external", ojson.New("url", fw, "macs", []any{m})); err != nil {
					return nil, err
				}
				return ojson.New("upgrading", m, "firmware_url", fw), nil
			}
			if _, err := client.Cmd(ctx, c.site(a), "devmgr", "upgrade", ojson.New("mac", m)); err != nil {
				return nil, err
			}
			return ojson.New("upgrading", m), nil
		})

	c.add(Def{
		Toolset: "devices", Name: "unifi_rolling_upgrade", Title: "Start/cancel rolling upgrade",
		Description: "Start a rolling (one-at-a-time) firmware upgrade of all devices of the given types to the latest firmware, or cancel one in progress.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("action", Enum("start", "cancel")),
			P("types", Array(Enum("uap", "usw", "ugw", "uxg")).Opt().D("Device types to upgrade when starting (default all: uap, usw, ugw, uxg)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			if a.Str("action") == "cancel" {
				if _, err := client.Cmd(ctx, s, "devmgr", "unset-rollupgrade", nil); err != nil {
					return nil, err
				}
				return ojson.New("rolling_upgrade", "cancelled"), nil
			}
			types := ojson.Coalesce(a.Get("types"), ojson.Strings([]string{"uap", "usw", "ugw", "uxg"}))
			if _, err := client.SitePost(ctx, s, "cmd/devmgr/set-rollupgrade", types); err != nil {
				return nil, err
			}
			return ojson.New("rolling_upgrade", "started", "types", types), nil
		},
	})

	devCmd("unifi_provision_device", "Force provision device",
		"Force the controller to re-push configuration to a device.",
		Write, true, []Prop{macArg()},
		func(ctx context.Context, a Args, m string) (any, error) {
			if _, err := client.Cmd(ctx, c.site(a), "devmgr", "force-provision", ojson.New("mac", m)); err != nil {
				return nil, err
			}
			return ojson.New("provisioning", m), nil
		})

	devCmd("unifi_adopt_device", "Adopt device",
		"Adopt a device that is pending adoption (see unifi_list_devices, state=pending_adoption).",
		Write, true, []Prop{macArg()},
		func(ctx context.Context, a Args, m string) (any, error) {
			if _, err := client.Cmd(ctx, c.site(a), "devmgr", "adopt", ojson.New("mac", m)); err != nil {
				return nil, err
			}
			return ojson.New("adopting", m), nil
		})

	devCmd("unifi_move_device", "Move device to another site",
		"Move an adopted device from this site to another site on the same controller.",
		Write, true,
		[]Prop{macArg(), P("target_site", Str().D("Destination site: its name (e.g. 'branch'), description, or 24-character _id (see unifi_list_sites)"))},
		func(ctx context.Context, a Args, m string) (any, error) {
			siteID, err := resolveSiteID(ctx, client, a.Str("target_site"))
			if err != nil {
				return nil, err
			}
			if _, err := client.Cmd(ctx, c.site(a), "sitemgr", "move-device", ojson.New("mac", m, "site", siteID)); err != nil {
				return nil, err
			}
			return ojson.New("moved", m, "target_site_id", siteID), nil
		})

	devCmd("unifi_forget_device", "Forget (remove) device",
		"Remove a device from the site. It is factory-reset when next reachable and must be re-adopted to be used again.",
		Delete, false, []Prop{macArg()},
		func(ctx context.Context, a Args, m string) (any, error) {
			if _, err := client.Cmd(ctx, c.site(a), "sitemgr", "delete-device", ojson.New("mac", m)); err != nil {
				return nil, err
			}
			return ojson.New("forgotten", m), nil
		})

	devCmd("unifi_run_rf_scan", "Run RF scan",
		"Start an RF/spectrum scan on an access point. Its radios go offline for a few minutes while scanning, disconnecting clients. Fetch results with unifi_get_rf_scan.",
		Write, true, []Prop{apMac},
		func(ctx context.Context, a Args, m string) (any, error) {
			if _, err := client.Cmd(ctx, c.site(a), "devmgr", "spectrum-scan", ojson.New("mac", m)); err != nil {
				return nil, err
			}
			return ojson.New("scanning", m), nil
		})

	c.add(Def{
		Toolset: "devices", Name: "unifi_save_ap_group", Title: "Create/update AP group",
		Destructive: boolPtr(true),
		Description: "Create an access point group (omit id) or update one (pass id). device_macs replaces the group's member list when given.",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("id", Str().Opt().D("AP group ID to update; omit to create")),
			P("name", Str().MinLen(1).Opt().D("Group name (required when creating)")),
			P("device_macs", Array(Str()).Opt().D("MAC addresses of the APs in the group (replaces the current list)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			if id == "" && !a.Truthy("name") {
				return nil, errorf("name is required when creating an AP group")
			}
			var deviceMacs any = U
			if list := a.Arr("device_macs"); list != nil {
				norm, err := macs(list)
				if err != nil {
					return nil, err
				}
				deviceMacs = norm
			}
			body := unifi.Defined("name", a.Get("name"), "device_macs", deviceMacs)
			if id == "" && ojson.IsNullish(body.Get("device_macs")) {
				body.Set("device_macs", []any{})
			}
			if id != "" && body.Len() == 0 {
				return nil, errorf("Nothing to update")
			}
			saved, err := v2Save(ctx, client, c.site(a), "apgroups", id, body)
			if err != nil {
				return nil, err
			}
			return summarizeAPGroup(asObj(saved)), nil
		},
	})

	c.add(Def{
		Toolset: "devices", Name: "unifi_delete_ap_group", Title: "Delete AP group",
		Description: "Delete an access point group. WiFi networks assigned to it must be moved to another group first.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("AP group")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			if err := v2Delete(ctx, client, c.site(a), "apgroups", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id")), nil
		},
	})
}
