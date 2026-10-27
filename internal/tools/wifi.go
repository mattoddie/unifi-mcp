package tools

import (
	"context"
	"regexp"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

func summarizeWlanFull(w *Obj, includeSecrets bool) *Obj {
	g := w.Get
	var wpa3 any = U
	if ojson.Truthy(g("wpa3_support")) {
		wpa3 = ojson.If(ojson.Truthy(g("wpa3_transition")), "transition", "only")
	}
	out := ojson.Spread(unifi.SummarizeWlan(w, includeSecrets))
	for _, kv := range [][2]any{
		{"wpa3", wpa3},
		{"usergroup_id", g("usergroup_id")},
		{"radius_profile_id", ojson.If(ojson.StrictEq(g("security"), "wpaeap"), g("radiusprofile_id"), U)},
		{"fast_roaming", ojson.OrUndefined(g("fast_roaming_enabled"))},
		{"bss_transition", ojson.OrUndefined(g("bss_transition"))},
		{"min_rate_2g_kbps", ojson.If(ojson.Truthy(g("minrate_ng_enabled")), g("minrate_ng_data_rate_kbps"), U)},
		{"min_rate_5g_kbps", ojson.If(ojson.Truthy(g("minrate_na_enabled")), g("minrate_na_data_rate_kbps"), U)},
		{"mac_filter_list", ojson.If(ojson.Truthy(g("mac_filter_enabled")) && ojson.HasLength(g("mac_filter_list")), g("mac_filter_list"), U)},
	} {
		out.Set(kv[0].(string), kv[1])
	}
	return unifi.CompactObj(out)
}

var qrSpecial = regexp.MustCompile(`([\\;,:"])`)

// qrEscape escapes a value for a WIFI: QR payload (backslash-escape \ ; , : ").
func qrEscape(value string) string {
	return qrSpecial.ReplaceAllString(value, `\$1`)
}

var allAPs = regexp.MustCompile(`(?i)all aps`)

// createDefaults are the defaults the controller needs on create: default bandwidth profile, LAN and AP group.
func createDefaults(ctx context.Context, client *unifi.Client, site string) *Obj {
	groups, err := restList(ctx, client, site, "usergroup")
	if err != nil {
		groups = []any{}
	}
	nets, err := restList(ctx, client, site, "networkconf")
	if err != nil {
		nets = []any{}
	}
	apGroups, apErr := v2List(ctx, client, site, "apgroups")
	out := ojson.New()
	usergroup := ojson.Coalesce(
		ojson.Find(groups, func(g any) bool { return ojson.Truthy(ojson.Get(g, "attr_no_delete")) }),
		ojson.Find(groups, func(g any) bool { return ojson.StrictEq(ojson.Get(g, "name"), "Default") }),
		firstOf(groups),
	)
	if ojson.Truthy(usergroup) {
		out.Set("usergroup_id", ojson.Get(usergroup, "_id"))
	}
	lan := ojson.Coalesce(
		ojson.Find(nets, func(n any) bool {
			return ojson.StrictEq(ojson.Get(n, "purpose"), "corporate") && ojson.Truthy(ojson.Get(n, "attr_no_delete"))
		}),
		ojson.Find(nets, func(n any) bool { return ojson.StrictEq(ojson.Get(n, "purpose"), "corporate") }),
	)
	if ojson.Truthy(lan) {
		out.Set("networkconf_id", ojson.Get(lan, "_id"))
	}
	var defaultAPGroup any = U
	if apErr == nil {
		defaultAPGroup = ojson.Coalesce(
			ojson.Find(apGroups, func(g any) bool { return ojson.Truthy(ojson.Get(g, "attr_no_delete")) }),
			ojson.Find(apGroups, func(g any) bool {
				name, _ := ojson.Coalesce(ojson.Get(g, "name"), "").(string)
				return allAPs.MatchString(name)
			}),
		)
	}
	if ojson.Truthy(defaultAPGroup) {
		out.Set("ap_group_ids", []any{ojson.Get(defaultAPGroup, "_id")})
		out.Set("ap_group_mode", "all")
	} else {
		// Older controllers use WLAN groups instead of AP groups.
		res, err := client.SiteGet(ctx, site, "list/wlangroup", nil)
		wlanGroups := arr(res)
		if err != nil {
			wlanGroups = []any{}
		}
		wg := ojson.Coalesce(ojson.Find(wlanGroups, func(g any) bool { return ojson.Truthy(ojson.Get(g, "attr_no_delete")) }), firstOf(wlanGroups))
		if ojson.Truthy(wg) {
			out.Set("wlangroup_id", ojson.Get(wg, "_id"))
		}
	}
	return out
}

func firstOf(list []any) any {
	if len(list) == 0 {
		return U
	}
	return list[0]
}

func registerWifi(c *Context) {
	client := c.Client

	c.add(Def{
		Toolset: "wifi", Name: "unifi_list_wlans", Title: "List WiFi networks",
		Description: "List wireless networks (SSIDs) with security, bands, linked network and enabled state.",
		Input:       props(siteArg(), listing{}.args(), rawArg(), c.secretsArg(secretsPassphrases)),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "rest/wlanconf", nil)
			if err != nil {
				return nil, err
			}
			include := c.includeSecrets(a)
			return listing{}.result(a, arr(res), func(w *Obj) any { return summarizeWlanFull(w, include) }, rawWithSecrets(c, a)), nil
		},
	})

	c.add(Def{
		Toolset: "wifi", Name: "unifi_save_wlan", Title: "Create/update WiFi network",
		Description: "Create a WiFi network/SSID (no id) or update one (id; only the fields passed change): name, password, security, WPA3, VLAN network, bands, AP groups, " +
			"guest/hidden/isolation, bandwidth profile, MAC filter, fast roaming, minimum data rates, enabled. " +
			"On create, defaults are WPA2-PSK (or open without a passphrase), 2.4+5 GHz, the default network, bandwidth profile and all APs. " +
			"Changes reconnect clients on that SSID.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("id", idSchema("WLAN").Opt().D("WLAN ID to update; omit to create")),
			P("name", Str().MinLen(1).MaxLen(32).Opt().D("SSID (required on create)")),
			P("passphrase", Str().MinLen(8).MaxLen(63).Opt().D("WPA passphrase (8-63 characters)")),
			P("security", Enum("open", "wpapsk", "wpaeap").Opt().D("open, wpapsk (personal) or wpaeap (enterprise/RADIUS)")),
			P("wpa_mode", Enum("wpa2", "wpa1", "auto").Opt().D("WPA version for WPA2/WPA mixed (default wpa2)")),
			P("wpa3", Enum("off", "transition", "only").Opt().D("WPA3: off, transition (WPA2/WPA3 mixed) or only")),
			P("network_id", Str().Opt().D("Network (VLAN) the SSID bridges to (see unifi_list_networks)")),
			P("ap_group_ids", Array(Str()).Opt().D("AP groups that broadcast the SSID (default: all APs)")),
			P("bands", Array(Enum("2g", "5g", "6g")).Min(1).Opt().D("Radio bands, e.g. ['2g','5g']")),
			P("hidden", Bool().Opt().D("Hide the SSID")),
			P("guest", Bool().Opt().D("Apply guest policies (guest portal / restrictions)")),
			P("client_isolation", Bool().Opt().D("Block clients on this SSID from talking to each other")),
			P("bandwidth_profile_id", Str().Opt().D("Bandwidth profile (user group) ID, see unifi_list_bandwidth_profiles")),
			P("radius_profile_id", Str().Opt().D("RADIUS profile for security wpaeap, see unifi_list_radius_profiles")),
			P("mac_filter", Object(
				P("policy", Enum("allow", "deny").D("allow = only listed MACs may join; deny = listed MACs are blocked")),
				P("macs", Array(Str())),
			).Null().Opt().D("MAC address filter; null disables it")),
			P("fast_roaming", Bool().Opt().D("802.11r fast roaming")),
			P("bss_transition", Bool().Opt().D("802.11v BSS transition (helps steer clients between APs)")),
			P("min_rate_2g_kbps", Int().Positive().Null().Opt().D("Minimum 2.4 GHz data rate in kbps (e.g. 6000, 12000); null disables")),
			P("min_rate_5g_kbps", Int().Positive().Null().Opt().D("Minimum 5 GHz data rate in kbps; null disables")),
			P("enabled", Bool().Opt()),
			P("extra", extraSchema().D("Additional raw wlanconf fields, e.g. schedule_enabled + schedule_with_duration, uapsd_enabled, group_rekey, dtim_ng/dtim_na, pmf_mode, iapp_enabled. Merged last.")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			var existing *Obj
			if id != "" {
				var err error
				if existing, err = restGet(ctx, client, s, "wlanconf", id); err != nil {
					return nil, err
				}
			}
			if existing == nil && !a.Truthy("name") {
				return nil, errorf("name is required when creating a WLAN")
			}

			fallbackSecurity := "open"
			if a.Truthy("passphrase") {
				fallbackSecurity = "wpapsk"
			}
			security := ojson.Coalesce(a.Get("security"), existing.Get("security"), fallbackSecurity)
			if ojson.StrictEq(security, "wpapsk") && !a.Truthy("passphrase") && !ojson.Truthy(existing.Get("x_passphrase")) {
				return nil, errorf("passphrase is required for a wpapsk WLAN")
			}
			if ojson.StrictEq(security, "wpaeap") && !a.Truthy("radius_profile_id") && !ojson.Truthy(existing.Get("radiusprofile_id")) {
				return nil, errorf("radius_profile_id is required for a wpaeap (enterprise) WLAN")
			}

			body := unifi.Defined(
				"name", a.Get("name"),
				"x_passphrase", a.Get("passphrase"),
				"security", a.Get("security"),
				"wpa_mode", a.Get("wpa_mode"),
				"networkconf_id", a.Get("network_id"),
				"ap_group_ids", a.Get("ap_group_ids"),
				"ap_group_mode", ojson.If(a.Truthy("ap_group_ids"), "groups", U),
				"wlan_bands", a.Get("bands"),
				"hide_ssid", a.Get("hidden"),
				"is_guest", a.Get("guest"),
				"l2_isolation", a.Get("client_isolation"),
				"usergroup_id", a.Get("bandwidth_profile_id"),
				"radiusprofile_id", a.Get("radius_profile_id"),
				"fast_roaming_enabled", a.Get("fast_roaming"),
				"bss_transition", a.Get("bss_transition"),
				"enabled", a.Get("enabled"),
			)
			if wpa3 := a.Str("wpa3"); wpa3 != "" {
				body.Set("wpa3_support", wpa3 != "off").Set("wpa3_transition", wpa3 == "transition")
			}
			if a.Has("mac_filter") {
				if mf := a.Obj("mac_filter"); mf == nil {
					body.Set("mac_filter_enabled", false)
				} else {
					list, err := macs(arr(mf.Get("macs")))
					if err != nil {
						return nil, err
					}
					body.Set("mac_filter_enabled", true).Set("mac_filter_policy", mf.Get("policy")).Set("mac_filter_list", list)
				}
			}
			for _, r := range []struct{ arg, band string }{{"min_rate_2g_kbps", "ng"}, {"min_rate_5g_kbps", "na"}} {
				if !a.Has(r.arg) {
					continue
				}
				if a.Get(r.arg) == nil {
					body.Set("minrate_"+r.band+"_enabled", false)
				} else {
					body.Set("minrate_"+r.band+"_enabled", true).Set("minrate_"+r.band+"_data_rate_kbps", a.Get(r.arg))
				}
			}

			if existing == nil {
				defaults := createDefaults(ctx, client, s)
				body.Assign(ojson.Spread(defaults, ojson.New(
					"enabled", true,
					"security", security,
					"wpa_mode", "wpa2",
					"wpa_enc", "ccmp",
					"wlan_bands", ojson.Strings([]string{"2g", "5g"}),
					"hide_ssid", false,
					"is_guest", false,
				), body))
				if ojson.StrictEq(security, "open") {
					body.Delete("x_passphrase")
				}
			}
			body.Assign(a.Get("extra"))

			saved, err := restSave(ctx, client, s, "wlanconf", id, body)
			if err != nil {
				return nil, err
			}
			return summarizeWlanFull(saved, false), nil
		},
	})

	c.add(Def{
		Toolset: "wifi", Name: "unifi_delete_wlan", Title: "Delete WiFi network",
		Description: "Delete a WiFi network (SSID). Clients connected to it are disconnected.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("WLAN")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			wlan, err := restGet(ctx, client, s, "wlanconf", a.Str("id"))
			if err != nil {
				return nil, err
			}
			if err := restDelete(ctx, client, s, "wlanconf", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id"), "name", wlan.Get("name")), nil
		},
	})

	c.add(Def{
		Toolset: "wifi", Name: "unifi_get_wifi_qr", Title: "Get WiFi join string (QR)",
		Description: "Get the standard WIFI: join string for an SSID, which phones understand when encoded as a QR code. NOTE: this reveals the WiFi password.",
		// The join string contains the passphrase, so the tool needs UNIFI_ALLOW_SECRETS.
		RevealsSecrets: true,
		Input:          props(siteArg(), idArg("WLAN")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			w, err := restGet(ctx, client, c.site(a), "wlanconf", a.Str("id"))
			if err != nil {
				return nil, err
			}
			if ojson.StrictEq(w.Get("security"), "wpaeap") {
				return nil, errorf("Enterprise (RADIUS) WLANs cannot be joined with a QR code")
			}
			typ := "WPA"
			switch {
			case ojson.StrictEq(w.Get("security"), "open"):
				typ = "nopass"
			case ojson.Truthy(w.Get("wpa3_support")) && !ojson.Truthy(w.Get("wpa3_transition")):
				typ = "SAE"
			}
			parts := []string{"T:" + typ, "S:" + qrEscape(ojson.String(w.Get("name")))}
			if typ != "nopass" {
				parts = append(parts, "P:"+qrEscape(ojson.String(ojson.Coalesce(w.Get("x_passphrase"), ""))))
			}
			if ojson.Truthy(w.Get("hide_ssid")) {
				parts = append(parts, "H:true")
			}
			return ojson.New("ssid", w.Get("name"), "wifi_string", "WIFI:"+strings.Join(parts, ";")+";;"), nil
		},
	})
}
