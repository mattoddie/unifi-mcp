package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

var geoFields = []string{
	"geo_ip_filtering_enabled",
	"geo_ip_filtering_block",
	"geo_ip_filtering_countries",
	"geo_ip_filtering_traffic_direction",
}

func geoSummary(usg any) *Obj {
	out := ojson.New()
	for _, f := range geoFields {
		out.Set(strings.Replace(f, "geo_ip_filtering_", "", 1), ojson.Get(usg, f))
	}
	return unifi.CompactObj(out)
}

func summarizeIPS(ips *Obj) *Obj {
	g := ips.Get
	return unifi.Compact(
		"ips_mode", g("ips_mode"),
		"enabled_categories", g("enabled_categories"),
		"enabled_networks", g("enabled_networks"),
		"advanced_filtering_preference", g("advanced_filtering_preference"),
		"memory_optimized", g("memory_optimized"),
		"honeypot_enabled", g("honeypot_enabled"),
		"dns_filtering", g("dns_filtering"),
		"ad_blocking_enabled", g("ad_blocking_enabled"),
		"restrict_tor", g("restrict_tor"),
		"restrict_torrents", g("restrict_torrents"),
	)
}

func summarizeContentFilter(f *Obj) *Obj {
	g := f.Get
	return unifi.Compact(
		"id", ojson.Coalesce(g("_id"), g("id")),
		"name", g("name"),
		"enabled", g("enabled"),
		"blocked_categories", ojson.Coalesce(g("blocked_categories"), g("categories")),
		"safe_search", g("safe_search"),
		"network_ids", g("network_ids"),
		"client_macs", g("client_macs"),
		"allow_list", g("allow_list"),
		"block_list", g("block_list"),
		"schedule", g("schedule"),
	)
}

func dpiSummary(dpi any) *Obj {
	return unifi.Compact("enabled", ojson.Get(dpi, "enabled"), "device_fingerprinting", ojson.Get(dpi, "fingerprintingEnabled"))
}

// nowMs is Date.now(); a variable so tests can fix it.
var nowMs = func() float64 { return float64(time.Now().UnixMilli()) }

func registerSecurity(c *Context) {
	client := c.Client

	c.add(Def{
		Toolset: "security", Name: "unifi_get_security_settings", Title: "Get security settings",
		Description: "Get threat management (IDS/IPS mode, categories, protected networks), traffic identification (DPI) and country/region blocking (geo-IP) settings. Optionally list the IPS categories the controller supports (Network 8+).",
		Input: props(
			siteArg(),
			P("include_categories", Bool().Opt().D("Also fetch the catalog of available IPS categories (v2 API, Network 8+)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			res, err := client.SiteGet(ctx, s, "rest/setting", nil)
			if err != nil {
				return nil, err
			}
			all := arr(res)
			byKey := func(k string) any {
				return ojson.Find(all, func(x any) bool { return ojson.StrictEq(ojson.Get(x, "key"), k) })
			}
			ips, dpi, usg := byKey("ips"), byKey("dpi"), byKey("usg")
			var geo *Obj
			if ojson.Truthy(usg) {
				geo = geoSummary(usg)
			}
			var threat any = "not available (no 'ips' setting; requires a UniFi gateway)"
			if ojson.Truthy(ips) {
				threat = summarizeIPS(asObj(ips))
			}
			result := ojson.New(
				"threat_management", threat,
				"traffic_identification", ojson.If(ojson.Truthy(dpi), dpiSummary(dpi), U),
				"country_blocking", ojson.If(geo != nil && geo.Len() > 0, geo, U),
			)
			if a.Truthy("include_categories") {
				memory := ojson.StrictEq(ojson.Get(ips, "memory_optimized"), true)
				cats, err := client.V2Query(ctx, "GET", s, "settings/ips/available-categories", []unifi.Param{{Key: "memoryOptimized", Value: memory}})
				if err != nil {
					cats = "unavailable: " + err.Error()
				}
				result.Set("available_ips_categories", cats)
			}
			return result, nil
		},
	})

	c.add(Def{
		Toolset: "security", Name: "unifi_update_threat_management", Title: "Update threat management / DPI",
		Description: "Change IDS/IPS (threat management) and traffic identification settings. Only the fields given are changed. ips_mode: disabled, ids (detect only), ips (detect and block), ipsInline (inline blocking, newer gateways). Changing IPS mode can briefly interrupt traffic and raise gateway CPU load.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("ips_mode", Enum("disabled", "ids", "ips", "ipsInline").Opt().D("Threat management mode")),
			P("enabled_categories", Array(Str()).Opt().D("IPS signature categories to enable (see unifi_get_security_settings include_categories)")),
			P("enabled_networks", Array(Str()).Opt().D("Network IDs protected by threat management")),
			P("dpi_enabled", Bool().Opt().D("Traffic identification (DPI)")),
			P("device_fingerprinting", Bool().Opt().D("Device identification / fingerprinting")),
			P("extra", extraSchema().D("Additional raw fields for the 'ips' setting (e.g. honeypot or ad blocking keys on your version)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			ipsPatch := unifi.Defined(
				"ips_mode", a.Get("ips_mode"),
				"enabled_categories", a.Get("enabled_categories"),
				"enabled_networks", a.Get("enabled_networks"),
			).Assign(a.Get("extra"))
			if a.Str("ips_mode") == "disabled" && !a.Truthy("enabled_categories") {
				ipsPatch.Set("enabled_categories", []any{})
			}
			dpiPatch := unifi.Defined("enabled", a.Get("dpi_enabled"), "fingerprintingEnabled", a.Get("device_fingerprinting"))
			if ipsPatch.Len() == 0 && dpiPatch.Len() == 0 {
				return nil, errorf("Nothing to update")
			}
			out := ojson.New()
			if ipsPatch.Len() > 0 {
				ips, err := updateSetting(ctx, client, s, "ips", ipsPatch)
				if err != nil {
					return nil, err
				}
				out.Set("threat_management", summarizeIPS(ips))
			}
			if dpiPatch.Len() > 0 {
				dpi, err := updateSetting(ctx, client, s, "dpi", dpiPatch)
				if err != nil {
					return nil, err
				}
				out.Set("traffic_identification", dpiSummary(dpi))
			}
			return out, nil
		},
	})

	c.add(Def{
		Toolset: "security", Name: "unifi_update_country_blocking", Title: "Update country/region blocking",
		Description: "Configure geo-IP (country/region) blocking on the gateway (stored in the 'usg' site setting). Field names are version-dependent: on some Network 9+ releases region blocking moved into the zone-based firewall UI; check unifi_get_security_settings first.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("enabled", Bool().Opt().D("Turn country blocking on or off")),
			P("mode", Enum("block", "allow").Opt().D("block = block listed countries; allow = allow only listed countries")),
			P("countries", Array(Str().MinLen(2).MaxLen(2)).Opt().D("ISO 3166-1 alpha-2 country codes, e.g. ['CN','RU']")),
			P("direction", Enum("both", "ingress", "egress").Opt().D("Traffic direction to filter")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			var countries any = U
			if list := a.Arr("countries"); list != nil {
				codes := make([]string, len(list))
				for i, cc := range list {
					codes[i] = strings.ToUpper(ojson.String(cc))
				}
				countries = strings.Join(codes, ",")
			}
			patch := unifi.Defined(
				"geo_ip_filtering_enabled", a.Get("enabled"),
				"geo_ip_filtering_block", a.Get("mode"),
				"geo_ip_filtering_countries", countries,
				"geo_ip_filtering_traffic_direction", a.Get("direction"),
			)
			if patch.Len() == 0 {
				return nil, errorf("Nothing to update")
			}
			usg, err := updateSetting(ctx, client, c.site(a), "usg", patch)
			if err != nil {
				return nil, err
			}
			return geoSummary(usg), nil
		},
	})

	c.add(Def{
		Toolset: "security", Name: "unifi_list_threats", Title: "List detected threats",
		Description: "List IDS/IPS threat events detected by the gateway (newest first): signature, category, source/destination, action taken.",
		Input: props(
			siteArg(),
			P("hours", Number().Positive().Max(24*90).Opt().D("Look back this many hours (default 24)")),
			limitArg(100),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			end := nowMs()
			limit := a.NumOr("limit", 100)
			res, err := client.SitePost(ctx, c.site(a), "stat/ips/event", ojson.New(
				"start", end-a.NumOr("hours", 24)*3600000,
				"end", end,
				"_limit", limit,
			))
			if err != nil {
				return nil, err
			}
			events := arr(res)
			ts := func(e any) float64 {
				return ojson.ToNumber(ojson.Coalesce(ojson.Get(e, "timestamp"), ojson.Get(e, "time"), 0.0))
			}
			ojson.SortStable(events, func(x, y any) float64 { return ts(y) - ts(x) })
			if int(limit) < len(events) {
				events = events[:int(limit)]
			}
			return mapObjs(events, func(e *Obj) any {
				g := e.Get
				return unifi.Compact(
					"time", ojson.Coalesce(unifi.IsoTime(ojson.Coalesce(g("timestamp"), g("time"))), g("datetime")),
					"signature", ojson.Coalesce(g("inner_alert_signature"), g("msg")),
					"category", ojson.Coalesce(g("inner_alert_category"), g("catname")),
					"severity", g("inner_alert_severity"),
					"action", g("inner_alert_action"),
					"protocol", g("proto"),
					"app_protocol", g("app_proto"),
					"src", unifi.Compact("ip", g("src_ip"), "port", g("src_port"), "mac", g("src_mac"), "country", g("srcipCountry")),
					"dst", unifi.Compact("ip", ojson.Coalesce(g("dest_ip"), g("dst_ip")), "port", ojson.Coalesce(g("dest_port"), g("dst_port")), "mac", g("dst_mac"), "country", g("dstipCountry")),
					"host", g("host"),
				)
			}), nil
		},
	})

	// ---------------------------------------------------------------- content filtering (v2)

	c.add(Def{
		Toolset: "security", Name: "unifi_list_content_filters", Title: "List content filters",
		Description: "List content filtering profiles (blocked categories, safe search, target networks/clients). v2 API, Network 8.1+.",
		Input:       props(siteArg(), P("raw", Bool().Opt().D("Return the full controller objects"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			filters, err := v2List(ctx, client, c.site(a), "content-filtering")
			if err != nil {
				return nil, err
			}
			if a.Truthy("raw") {
				return filters, nil
			}
			return mapObjs(filters, func(f *Obj) any { return summarizeContentFilter(f) }), nil
		},
	})

	c.add(Def{
		Toolset: "security", Name: "unifi_save_content_filter", Title: "Create/update content filter",
		Destructive: boolPtr(true),
		Description: "Create a content filtering profile (no id) or update one (id; only given fields change). v2 API, Network 8.1+.",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("id", idSchema("Content filter").Opt()),
			P("name", Str().Opt()),
			P("enabled", Bool().Opt()),
			P("blocked_categories", Array(Str()).Opt().D("Category identifiers as used by the controller (see raw output of unifi_list_content_filters)")),
			P("safe_search", Array(Str()).Opt().D("Search engines to force safe search on, e.g. ['GOOGLE','BING','YOUTUBE']")),
			P("network_ids", Array(Str()).Opt().D("Networks the filter applies to")),
			P("client_macs", Array(Str()).Opt().D("Clients the filter applies to")),
			P("allow_list", Array(Str()).Opt().D("Domains always allowed")),
			P("block_list", Array(Str()).Opt().D("Domains always blocked")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			var clientMacs any = U
			if list := a.Arr("client_macs"); list != nil {
				clientMacs = ojson.Map(list, func(m any) any { return strings.ToLower(ojson.String(m)) })
			}
			fields := unifi.Defined(
				"name", a.Get("name"),
				"enabled", a.Get("enabled"),
				"blocked_categories", a.Get("blocked_categories"),
				"safe_search", a.Get("safe_search"),
				"network_ids", a.Get("network_ids"),
				"client_macs", clientMacs,
				"allow_list", a.Get("allow_list"),
				"block_list", a.Get("block_list"),
			).Assign(a.Get("extra"))
			if id != "" {
				list, err := v2List(ctx, client, s, "content-filtering")
				if err != nil {
					return nil, err
				}
				current := ojson.Find(list, func(f any) bool {
					return ojson.StrictEq(ojson.Coalesce(ojson.Get(f, "_id"), ojson.Get(f, "id")), id)
				})
				if ojson.IsUndefined(current) {
					return nil, fmt.Errorf("No content filter with id %s on site %s", id, s)
				}
				e, err := enc(id)
				if err != nil {
					return nil, err
				}
				merged := ojson.Spread(current, fields)
				updated, err := client.V2(ctx, "PUT", s, "content-filtering/"+e, merged)
				if err != nil {
					return nil, err
				}
				return summarizeContentFilter(asObj(ojson.Coalesce(updated, ojson.Spread(current, fields)))), nil
			}
			if !a.Truthy("name") {
				return nil, errorf("name is required when creating a content filter")
			}
			body := ojson.Spread(ojson.New(
				"enabled", true,
				"blocked_categories", []any{},
				"safe_search", []any{},
				"network_ids", []any{},
				"client_macs", []any{},
				"schedule", ojson.New("mode", "ALWAYS", "repeat_on_days", []any{}, "time_all_day", true),
			), fields)
			created, err := client.V2(ctx, "POST", s, "content-filtering/create", body)
			if err != nil {
				return nil, err
			}
			return summarizeContentFilter(asObj(ojson.Coalesce(created, body))), nil
		},
	})

	c.add(Def{
		Toolset: "security", Name: "unifi_delete_content_filter", Title: "Delete content filter",
		Description: "Delete a content filtering profile.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Content filter")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			e, err := enc(a.Str("id"))
			if err != nil {
				return nil, err
			}
			if _, err := client.V2(ctx, "DELETE", c.site(a), "content-filtering/"+e, U); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id")), nil
		},
	})
}
