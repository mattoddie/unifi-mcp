package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

var rulesets = []string{
	"WAN_IN", "WAN_OUT", "WAN_LOCAL",
	"LAN_IN", "LAN_OUT", "LAN_LOCAL",
	"GUEST_IN", "GUEST_OUT", "GUEST_LOCAL",
	"WANv6_IN", "WANv6_OUT", "WANv6_LOCAL",
	"LANv6_IN", "LANv6_OUT", "LANv6_LOCAL",
	"GUESTv6_IN", "GUESTv6_OUT", "GUESTv6_LOCAL",
}

func saveIDArg(what string) Prop {
	return P("id", Str().Opt().D(what+" ID to update (only the fields passed are changed). Omit to create a new one."))
}

// ---------------------------------------------------------------- shared helpers (also used by routing)

// targetDevices builds traffic rule/route target_devices from convenience args; Undefined when neither list is given.
func targetDevices(a Args) (any, error) {
	if a.Truthy("all_clients") {
		return []any{ojson.New("type", "ALL_CLIENTS")}, nil
	}
	clientMacs, networkIDs := a.Arr("client_macs"), a.Arr("network_ids")
	if clientMacs == nil && networkIDs == nil {
		return U, nil
	}
	targets := []any{}
	for _, m := range clientMacs {
		n, err := mac(m)
		if err != nil {
			return nil, err
		}
		targets = append(targets, ojson.New("type", "CLIENT", "client_mac", n))
	}
	for _, n := range networkIDs {
		targets = append(targets, ojson.New("type", "NETWORK", "network_id", n))
	}
	if len(targets) == 0 {
		return []any{ojson.New("type", "ALL_CLIENTS")}, nil
	}
	return targets, nil
}

func targetArgs() []Prop {
	return []Prop{
		P("all_clients", Bool().Opt().D("Apply to all clients (default on create when no clients/networks are given)")),
		P("client_macs", Array(Str()).Opt().D("Apply to these client MAC addresses")),
		P("network_ids", Array(Str()).Opt().D("Apply to all clients on these network IDs (see unifi_list_networks)")),
	}
}

func domainEntries(domains []any) any {
	if domains == nil {
		return U
	}
	return ojson.Map(domains, func(d any) any { return ojson.New("domain", d, "ports", []any{}, "port_ranges", []any{}) })
}

func ipEntries(ips []any) any {
	if ips == nil {
		return U
	}
	return ojson.Map(ips, func(ip any) any {
		version := "v4"
		if strings.Contains(ojson.String(ip), ":") {
			version = "v6"
		}
		return ojson.New("ip_or_subnet", ip, "ip_version", version, "ports", []any{}, "port_ranges", []any{})
	})
}

func summarizeTargets(targets any) any {
	list, ok := ojson.Arr(targets)
	if !ok || len(list) == 0 {
		return U
	}
	return ojson.Map(list, func(t any) any {
		switch ojson.Get(t, "type") {
		case "CLIENT":
			return "client:" + ojson.String(ojson.Get(t, "client_mac"))
		case "NETWORK":
			return "network:" + ojson.String(ojson.Get(t, "network_id"))
		}
		return ojson.Get(t, "type")
	})
}

func summarizeTrafficMatch(r *Obj) *Obj {
	g := r.Get
	pick := func(k, field string) any {
		if !ojson.HasLength(g(k)) {
			return U
		}
		return ojson.Map(arr(g(k)), func(x any) any {
			if _, ok := x.(string); ok {
				return x
			}
			return ojson.Get(x, field)
		})
	}
	var ranges any = U
	if ojson.HasLength(g("ip_ranges")) {
		ranges = ojson.Map(arr(g("ip_ranges")), func(i any) any {
			return ojson.String(ojson.Get(i, "ip_start")) + "-" + ojson.String(ojson.Get(i, "ip_stop"))
		})
	}
	return unifi.Compact(
		"domains", pick("domains", "domain"),
		"ip_addresses", pick("ip_addresses", "ip_or_subnet"),
		"ip_ranges", ranges,
		"regions", ojson.If(ojson.HasLength(g("regions")), g("regions"), U),
		"targets", summarizeTargets(g("target_devices")),
	)
}

// integrationSiteID resolves a site name to the official integration API site UUID.
func integrationSiteID(ctx context.Context, client *unifi.Client, site string) (string, error) {
	res, err := client.Request(ctx, "GET", "/integration/v1/sites", &unifi.Options{Body: U, Query: []unifi.Param{{Key: "limit", Value: 200.0}}})
	if err != nil {
		return "", err
	}
	sites := arr(ojson.Coalesce(ojson.Get(res, "data"), []any{}))
	match := ojson.Find(sites, func(s any) bool {
		return ojson.Includes([]any{ojson.Get(s, "internalReference"), ojson.Get(s, "name"), ojson.Get(s, "id")}, site)
	})
	id := ojson.Get(match, "id")
	if !ojson.Truthy(id) {
		return "", fmt.Errorf(`Could not find site "%s" in the integration API (/integration/v1/sites); pass its internalReference, name or id as site`, site)
	}
	return ojson.String(id), nil
}

func integrationZones(ctx context.Context, client *unifi.Client, siteID string) ([]any, error) {
	res, err := client.Request(ctx, "GET", "/integration/v1/sites/"+siteID+"/firewall/zones", &unifi.Options{Body: U, Query: []unifi.Param{{Key: "limit", Value: 200.0}}})
	if err != nil {
		return nil, err
	}
	return arr(ojson.Coalesce(ojson.Get(res, "data"), []any{})), nil
}

// resolveIntegrationZone maps a v2 zone _id (or an integration zone id/name) to the integration zone record.
func resolveIntegrationZone(ctx context.Context, client *unifi.Client, site, siteID, zoneID string) (*Obj, error) {
	zones, err := integrationZones(ctx, client, siteID)
	if err != nil {
		return nil, err
	}
	if direct := ojson.Find(zones, func(z any) bool { return ojson.StrictEq(ojson.Get(z, "id"), zoneID) }); !ojson.IsUndefined(direct) {
		return asObj(direct), nil
	}
	v2Zones, err := v2List(ctx, client, site, "firewall/zone")
	if err != nil {
		v2Zones = []any{}
	}
	v2 := ojson.Find(v2Zones, func(z any) bool {
		return ojson.StrictEq(ojson.Get(z, "_id"), zoneID) || ojson.StrictEq(ojson.Get(z, "name"), zoneID)
	})
	if ext := ojson.Get(v2, "external_id"); ojson.Truthy(ext) {
		if byExternal := ojson.Find(zones, func(z any) bool { return ojson.StrictEq(ojson.Get(z, "id"), ext) }); !ojson.IsUndefined(byExternal) {
			return asObj(byExternal), nil
		}
	}
	name := strings.ToLower(ojson.String(ojson.Coalesce(ojson.Get(v2, "name"), zoneID)))
	byName := ojson.Filter(zones, func(z any) bool {
		return strings.ToLower(ojson.String(ojson.Coalesce(ojson.Get(z, "name"), ""))) == name
	})
	if len(byName) == 1 {
		return asObj(byName[0]), nil
	}
	return nil, fmt.Errorf(`Could not map firewall zone "%s" to an integration API zone`, zoneID)
}

const integrationNote = "Uses the official integration API, so it requires API-key authentication (UNIFI_API_KEY) and Network 9.0+."

// ---------------------------------------------------------------- summaries

func summarizeRule(r *Obj) *Obj {
	g := r.Get
	var states []any
	for _, s := range [][2]string{{"state_new", "new"}, {"state_established", "established"}, {"state_related", "related"}, {"state_invalid", "invalid"}} {
		// [r.state_new && "new", ...].filter(Boolean)
		if ojson.Truthy(g(s[0])) {
			states = append(states, s[1])
		}
	}
	if states == nil {
		states = []any{}
	}
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"enabled", g("enabled"),
		"ruleset", g("ruleset"),
		"index", g("rule_index"),
		"action", g("action"),
		"protocol", g("protocol"),
		"src", unifi.Compact(
			"address", g("src_address"),
			"network_id", g("src_networkconf_id"),
			"group_ids", ojson.If(ojson.HasLength(g("src_firewallgroup_ids")), g("src_firewallgroup_ids"), U),
			"mac", g("src_mac_address"),
			"port", g("src_port"),
		),
		"dst", unifi.Compact(
			"address", g("dst_address"),
			"network_id", g("dst_networkconf_id"),
			"group_ids", ojson.If(ojson.HasLength(g("dst_firewallgroup_ids")), g("dst_firewallgroup_ids"), U),
			"port", g("dst_port"),
		),
		"states", states,
		"logging", ojson.OrUndefined(g("logging")),
	)
}

func summarizePortForward(r *Obj) *Obj {
	g := r.Get
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"enabled", g("enabled"),
		"protocol", g("proto"),
		"wan_interface", g("pfwd_interface"),
		"source", g("src"),
		"destination_ip", g("destination_ip"),
		"external_port", g("dst_port"),
		"forward_ip", g("fwd"),
		"forward_port", g("fwd_port"),
		"log", g("log"),
	)
}

func summarizeEndpoint(v any) any {
	if !ojson.Truthy(v) {
		return U
	}
	e := asObj(v)
	g := e.Get
	pmt := g("port_matching_type")
	return unifi.Compact(
		"zone_id", g("zone_id"),
		"match", g("matching_target"),
		"match_type", g("matching_target_type"),
		"ips", ojson.If(ojson.HasLength(g("ips")), g("ips"), U),
		"network_ids", ojson.If(ojson.HasLength(g("network_ids")), g("network_ids"), U),
		"client_macs", ojson.If(ojson.HasLength(g("client_macs")), g("client_macs"), U),
		"ip_group_id", g("ip_group_id"),
		"port", ojson.If(ojson.StrictEq(pmt, "SPECIFIC"), g("port"), U),
		"port_group_id", ojson.If(ojson.StrictEq(pmt, "OBJECT"), g("port_group_id"), U),
		"port_match", ojson.If(ojson.Truthy(pmt) && !ojson.StrictEq(pmt, "ANY"), pmt, U),
		"negate", ojson.OrUndefined(ojson.Or(g("match_opposite_ips"), g("match_opposite_ports"))),
	)
}

// schedule returns a non-default schedule (mode other than ALWAYS), or Undefined.
func nonDefaultSchedule(s any) any {
	mode := ojson.Get(s, "mode")
	if ojson.Truthy(mode) && !ojson.StrictEq(mode, "ALWAYS") {
		return s
	}
	return U
}

func summarizePolicy(p *Obj) *Obj {
	g := p.Get
	states := g("connection_state_type")
	if ojson.StrictEq(states, "CUSTOM") {
		states = g("connection_states")
	}
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"description", g("description"),
		"enabled", g("enabled"),
		"action", g("action"),
		"index", g("index"),
		"predefined", ojson.OrUndefined(g("predefined")),
		"protocol", g("protocol"),
		"ip_version", g("ip_version"),
		"connection_states", states,
		"source", summarizeEndpoint(g("source")),
		"destination", summarizeEndpoint(g("destination")),
		"schedule", nonDefaultSchedule(g("schedule")),
		"logging", ojson.OrUndefined(g("logging")),
	)
}

func summarizeTrafficRule(r *Obj) *Obj {
	g := r.Get
	var bw any = U
	if ojson.Truthy(ojson.Get(g("bandwidth_limit"), "enabled")) {
		bw = ojson.New("down_kbps", ojson.Get(g("bandwidth_limit"), "download_limit_kbps"), "up_kbps", ojson.Get(g("bandwidth_limit"), "upload_limit_kbps"))
	}
	out := ojson.New("id", g("_id"), "description", g("description"), "enabled", g("enabled"), "action", g("action"), "matching_target", g("matching_target"))
	out.Assign(summarizeTrafficMatch(r))
	out.Set("app_ids", ojson.If(ojson.HasLength(g("app_ids")), g("app_ids"), U))
	out.Set("app_category_ids", ojson.If(ojson.HasLength(g("app_category_ids")), g("app_category_ids"), U))
	out.Set("bandwidth_limit", bw)
	out.Set("schedule", nonDefaultSchedule(g("schedule")))
	return unifi.CompactObj(out)
}

// ---------------------------------------------------------------- input schemas

func policyEndpoint() *Schema {
	return LooseObject(
		P("zone_id", Str().Opt().D("Firewall zone ID (see unifi_list_firewall_zones)")),
		P("matching_target", Enum("ANY", "IP", "NETWORK", "CLIENT", "REGION", "DOMAIN", "APP", "APP_CATEGORY", "WEB").Opt().D("What to match inside the zone (default ANY)")),
		P("matching_target_type", Enum("ANY", "SPECIFIC", "OBJECT").Opt().D("SPECIFIC with ips, OBJECT with network_ids or ip_group_id")),
		P("ips", Array(Str()).Opt().D("IPs/CIDRs/ranges for matching_target=IP, matching_target_type=SPECIFIC")),
		P("network_ids", Array(Str()).Opt().D("Network IDs for matching_target=NETWORK")),
		P("client_macs", Array(Str()).Opt().D("Client MACs for matching_target=CLIENT")),
		P("port_matching_type", Enum("ANY", "SPECIFIC", "OBJECT").Opt().D("SPECIFIC with port, OBJECT with port_group_id")),
		P("port", Str().Opt().D("Port(s) as one string, e.g. '53' or '80,443' or '8000-8100'")),
		P("port_group_id", Str().Opt().D("Port group ID (firewall group of type port-group)")),
	).D("Policy endpoint; extra controller fields are passed through")
}

func normalizeEndpoint(e *Obj) (*Obj, error) {
	if e == nil {
		return nil, nil
	}
	out := ojson.Spread(e)
	if s, ok := out.Get("matching_target").(string); ok {
		out.Set("matching_target", strings.ToUpper(s))
	}
	if list, ok := ojson.Arr(out.Get("client_macs")); ok {
		norm, err := macs(list)
		if err != nil {
			return nil, err
		}
		out.Set("client_macs", norm)
	}
	if !ojson.IsUndefined(out.Get("port")) && ojson.IsUndefined(out.Get("port_matching_type")) {
		out.Set("port_matching_type", "SPECIFIC")
	}
	if !ojson.IsUndefined(out.Get("port_group_id")) && ojson.IsUndefined(out.Get("port_matching_type")) {
		out.Set("port_matching_type", "OBJECT")
	}
	if ojson.HasLength(out.Get("ips")) && ojson.IsUndefined(out.Get("matching_target")) {
		out.Set("matching_target", "IP")
	}
	if ojson.HasLength(out.Get("ips")) && ojson.IsUndefined(out.Get("matching_target_type")) {
		out.Set("matching_target_type", "SPECIFIC")
	}
	if ojson.HasLength(out.Get("network_ids")) && ojson.IsUndefined(out.Get("matching_target")) {
		out.Set("matching_target", "NETWORK")
	}
	if ojson.HasLength(out.Get("network_ids")) && ojson.IsUndefined(out.Get("matching_target_type")) {
		out.Set("matching_target_type", "OBJECT")
	}
	if ojson.HasLength(out.Get("client_macs")) && ojson.IsUndefined(out.Get("matching_target")) {
		out.Set("matching_target", "CLIENT")
	}
	return out, nil
}

// savedOr returns saved when it is a plain object, otherwise fallback.
func savedOr(saved any, fallback *Obj) *Obj {
	if o, ok := ojson.AsObj(saved); ok {
		return o
	}
	return fallback
}

func registerFirewall(c *Context) {
	client := c.Client

	// ---------------------------------------------------------------- port forwards

	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_port_forwards", Title: "List port forwards",
		Description: "List port forwarding rules.",
		Input:       props(siteArg(), listing{}.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := restList(ctx, client, c.site(a), "portforward")
			if err != nil {
				return nil, err
			}
			return listing{}.result(a, list, func(r *Obj) any { return summarizePortForward(r) }, nil), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_port_forward", Title: "Create or update port forward",
		Description: "Create a port forward (name, external_port and forward_ip required) or update one by id. Defaults on create: WAN interface 'wan', protocol tcp_udp, any source, enabled.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("Port forward"),
			P("name", Str().Opt()),
			P("enabled", Bool().Opt()),
			P("wan_interface", Enum("wan", "wan2", "both").Opt().D("WAN interface to listen on")),
			P("protocol", Enum("tcp_udp", "tcp", "udp").Opt()),
			P("source", Str().Opt().D("Allowed source: 'any' or an IP/CIDR")),
			P("destination_ip", Str().Opt().D("WAN IP to match ('any' for all WAN IPs; newer Network versions)")),
			P("external_port", Str().Opt().D("External port or range, e.g. '443' or '8000-8100'")),
			P("forward_ip", Str().Opt().D("Internal IP to forward to")),
			P("forward_port", Str().Opt().D("Internal port or range (defaults to external_port on create)")),
			P("log", Bool().Opt().D("Log forwarded connections")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			body := unifi.Defined(
				"name", a.Get("name"),
				"enabled", a.Get("enabled"),
				"pfwd_interface", a.Get("wan_interface"),
				"proto", a.Get("protocol"),
				"src", a.Get("source"),
				"destination_ip", a.Get("destination_ip"),
				"dst_port", a.Get("external_port"),
				"fwd", a.Get("forward_ip"),
				"fwd_port", a.Get("forward_port"),
				"log", a.Get("log"),
			)
			if id == "" {
				if !a.Truthy("name") || !a.Truthy("external_port") || !a.Truthy("forward_ip") {
					return nil, errorf("name, external_port and forward_ip are required to create a port forward")
				}
				body = ojson.Spread(ojson.New("enabled", true, "pfwd_interface", "wan", "proto", "tcp_udp", "src", "any", "log", false, "fwd_port", a.Get("external_port")), body)
			}
			saved, err := restSave(ctx, client, c.site(a), "portforward", id, ojson.Spread(body, a.Get("extra")))
			if err != nil {
				return nil, err
			}
			return summarizePortForward(saved), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_port_forward", Title: "Delete port forward",
		Description: "Delete a port forwarding rule.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Port forward")),
		Handler:     deleteHandler(c, "portforward", false),
	})

	// ---------------------------------------------------------------- classic firewall rules

	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_firewall_rules", Title: "List firewall rules (legacy)",
		Description: "List classic firewall rules (WAN_IN, LAN_IN, ...). On Network 9+ with zone-based firewall use unifi_list_firewall_policies instead.",
		Input:       props(siteArg(), listing{}.args(), rawArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			rules, err := restList(ctx, client, c.site(a), "firewallrule")
			if err != nil {
				return nil, err
			}
			ojson.SortStable(rules, func(x, y any) float64 {
				if cmp := ojson.LocaleCompare(ojson.String(ojson.Get(x, "ruleset")), ojson.String(ojson.Get(y, "ruleset"))); cmp != 0 {
					return float64(cmp)
				}
				return ojson.ToNumber(ojson.Coalesce(ojson.Get(x, "rule_index"), 0.0)) - ojson.ToNumber(ojson.Coalesce(ojson.Get(y, "rule_index"), 0.0))
			})
			return listing{}.result(a, rules, func(r *Obj) any { return summarizeRule(r) }, asIs), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_firewall_rule", Title: "Create or update firewall rule (legacy)",
		Description: "Create a classic firewall rule (name, ruleset and action required) or update one by id. Not for zone-based firewall (Network 9+ with zones migrated) — use unifi_save_firewall_policy there. " +
			"On create, rule_index defaults to the next free index from 2000 in the ruleset (rules before the predefined ones).",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("Firewall rule"),
			P("name", Str().Opt()),
			P("enabled", Bool().Opt()),
			P("ruleset", Enum(rulesets...).Opt()),
			P("rule_index", Int().Opt().D("Order within the ruleset (2000-2999 run before predefined rules, 4000+ after)")),
			P("action", Enum("accept", "drop", "reject").Opt()),
			P("protocol", Str().Opt().D("all (default), tcp, udp, tcp_udp, icmp, or another protocol name/number")),
			P("protocol_match_excepted", Bool().Opt().D("Match every protocol except the one given")),
			P("src_network_id", Str().Opt().D("Source network ID")),
			P("src_network_type", Enum("NETv4", "ADDRv4").Opt().D("NETv4 = whole subnet, ADDRv4 = gateway IP of the network")),
			P("src_address", Str().Opt().D("Source IP/CIDR")),
			P("src_mac", Str().Opt().D("Source MAC address")),
			P("src_group_ids", Array(Str()).Opt().D("Source firewall group IDs (address and/or port group)")),
			P("src_port", Str().Opt().D("Source port(s) (tcp/udp only)")),
			P("dst_network_id", Str().Opt()),
			P("dst_network_type", Enum("NETv4", "ADDRv4").Opt()),
			P("dst_address", Str().Opt().D("Destination IP/CIDR")),
			P("dst_group_ids", Array(Str()).Opt().D("Destination firewall group IDs")),
			P("dst_port", Str().Opt().D("Destination port(s) (tcp/udp only)")),
			P("states", Array(Enum("new", "established", "related", "invalid")).Opt().D("Connection states to match (none = all states)")),
			P("logging", Bool().Opt()),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			var srcMac any = U
			if a.Has("src_mac") {
				srcMac = a.Get("src_mac")
				if a.Truthy("src_mac") {
					m, err := mac(a.Get("src_mac"))
					if err != nil {
						return nil, err
					}
					srcMac = m
				}
			}
			body := unifi.Defined(
				"name", a.Get("name"),
				"enabled", a.Get("enabled"),
				"ruleset", a.Get("ruleset"),
				"rule_index", a.Get("rule_index"),
				"action", a.Get("action"),
				"protocol", a.Get("protocol"),
				"protocol_match_excepted", a.Get("protocol_match_excepted"),
				"src_networkconf_id", a.Get("src_network_id"),
				"src_networkconf_type", a.Get("src_network_type"),
				"src_address", a.Get("src_address"),
				"src_mac_address", srcMac,
				"src_firewallgroup_ids", a.Get("src_group_ids"),
				"src_port", a.Get("src_port"),
				"dst_networkconf_id", a.Get("dst_network_id"),
				"dst_networkconf_type", a.Get("dst_network_type"),
				"dst_address", a.Get("dst_address"),
				"dst_firewallgroup_ids", a.Get("dst_group_ids"),
				"dst_port", a.Get("dst_port"),
				"logging", a.Get("logging"),
			)
			if states := a.Arr("states"); states != nil {
				for _, st := range []string{"new", "established", "related", "invalid"} {
					body.Set("state_"+st, ojson.Includes(states, st))
				}
			}
			if id == "" {
				if !a.Truthy("name") || !a.Truthy("ruleset") || !a.Truthy("action") {
					return nil, errorf("name, ruleset and action are required to create a firewall rule")
				}
				if ojson.IsUndefined(body.Get("rule_index")) {
					existing, err := restList(ctx, client, s, "firewallrule")
					if err != nil {
						return nil, err
					}
					var used []float64
					for _, r := range existing {
						idx := ojson.Get(r, "rule_index")
						n := ojson.ToNumber(idx)
						if ojson.StrictEq(ojson.Get(r, "ruleset"), a.Get("ruleset")) && !ojson.IsNullish(idx) && n >= 2000 && n < 3000 {
							used = append(used, n)
						}
					}
					next := 2000.0
					if len(used) > 0 {
						sort.Float64s(used)
						next = used[len(used)-1] + 1
					}
					body.Set("rule_index", next)
				}
				body = ojson.Spread(ojson.New(
					"enabled", true,
					"protocol", "all",
					"protocol_match_excepted", false,
					"logging", false,
					"state_new", false,
					"state_established", false,
					"state_related", false,
					"state_invalid", false,
					"ipsec", "",
					"src_firewallgroup_ids", []any{},
					"src_mac_address", "",
					"src_address", "",
					"src_networkconf_id", "",
					"src_networkconf_type", "NETv4",
					"dst_firewallgroup_ids", []any{},
					"dst_address", "",
					"dst_networkconf_id", "",
					"dst_networkconf_type", "NETv4",
				), body)
			}
			saved, err := restSave(ctx, client, s, "firewallrule", id, ojson.Spread(body, a.Get("extra")))
			if err != nil {
				return nil, err
			}
			return summarizeRule(saved), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_firewall_rule", Title: "Delete firewall rule (legacy)",
		Description: "Delete a classic firewall rule.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Firewall rule")),
		Handler:     deleteHandler(c, "firewallrule", false),
	})

	// ---------------------------------------------------------------- firewall groups

	groupSummary := func(g *Obj) *Obj {
		return unifi.Compact("id", g.Get("_id"), "name", g.Get("name"), "type", g.Get("group_type"), "members", g.Get("group_members"))
	}
	groupList := listing{Search: []string{"name", "type", "members"}}

	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_firewall_groups", Title: "List firewall groups",
		Description: "List firewall groups (address groups, IPv6 address groups and port groups) referenced by firewall rules and policies.",
		Input:       props(siteArg(), groupList.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := restList(ctx, client, c.site(a), "firewallgroup")
			if err != nil {
				return nil, err
			}
			return groupList.result(a, list, func(g *Obj) any { return groupSummary(g) }, nil), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_firewall_group", Title: "Create or update firewall group",
		Description: "Create a firewall group (name and type required) or update one by id. members replaces the whole member list. The type of an existing group cannot be changed.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("Firewall group"),
			P("name", Str().Opt()),
			P("type", Enum("address-group", "ipv6-address-group", "port-group").Opt()),
			P("members", Array(Str()).Opt().D("IPs/CIDRs (address groups) or ports/ranges like '80' or '8000-8100' (port groups)")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			body := unifi.Defined("name", a.Get("name"), "group_type", a.Get("type"), "group_members", a.Get("members"))
			if id != "" {
				if a.Truthy("type") {
					current, err := restGet(ctx, client, s, "firewallgroup", id)
					if err != nil {
						return nil, err
					}
					if !ojson.StrictEq(current.Get("group_type"), a.Get("type")) {
						return nil, fmt.Errorf("The type of an existing firewall group cannot be changed (it is %s)", ojson.String(current.Get("group_type")))
					}
				}
			} else {
				if !a.Truthy("name") || !a.Truthy("type") {
					return nil, errorf("name and type are required to create a firewall group")
				}
				body = ojson.Spread(ojson.New("group_members", []any{}), body)
			}
			g, err := restSave(ctx, client, s, "firewallgroup", id, ojson.Spread(body, a.Get("extra")))
			if err != nil {
				return nil, err
			}
			return groupSummary(g), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_firewall_group", Title: "Delete firewall group",
		Description: "Delete a firewall group. The controller refuses to delete groups still referenced by rules or policies.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Firewall group")),
		Handler:     deleteHandler(c, "firewallgroup", false),
	})

	// ---------------------------------------------------------------- zone-based firewall (Network 9.0+)

	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_firewall_policies", Title: "List firewall policies (zone-based)",
		Description: "List zone-based firewall policies (UniFi Network 9.0+). Predefined (system) policies are hidden unless include_predefined is set.",
		Input: props(
			siteArg(),
			P("include_predefined", Bool().Opt().D("Also return predefined/system policies")),
			listing{}.args(),
			rawArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			policies, err := v2List(ctx, client, c.site(a), "firewall-policies")
			if err != nil {
				return nil, err
			}
			if !a.Truthy("include_predefined") {
				policies = ojson.Filter(policies, func(p any) bool { return !ojson.Truthy(ojson.Get(p, "predefined")) })
			}
			ojson.SortStable(policies, func(x, y any) float64 {
				return ojson.ToNumber(ojson.Coalesce(ojson.Get(x, "index"), 0.0)) - ojson.ToNumber(ojson.Coalesce(ojson.Get(y, "index"), 0.0))
			})
			return listing{}.result(a, policies, func(p *Obj) any { return summarizePolicy(p) }, asIs), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_firewall_policy", Title: "Create or update firewall policy (zone-based)",
		Description: "Create a zone-based firewall policy (name, action, source and destination required) or update one by id (Network 9.0+). " +
			"source/destination: {zone_id, matching_target: ANY|IP|NETWORK|CLIENT|REGION|..., ips | network_ids | client_macs, port_matching_type, port | port_group_id}; on update they are merged into the current endpoint. " +
			"Use unifi_list_firewall_zones for zone IDs. Predefined policies cannot be changed.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("Firewall policy"),
			P("name", Str().Opt()),
			P("description", Str().Opt()),
			P("enabled", Bool().Opt()),
			P("action", Enum("ALLOW", "BLOCK", "REJECT").Opt()),
			P("source", policyEndpoint().Opt()),
			P("destination", policyEndpoint().Opt()),
			P("protocol", Str().Opt().D("all (default), tcp, udp, tcp_udp, icmp, icmpv6, ...")),
			P("ip_version", Enum("BOTH", "IPV4", "IPV6").Opt()),
			P("connection_state_type", Enum("ALL", "RESPOND_ONLY", "CUSTOM").Opt()),
			P("connection_states", Array(Enum("NEW", "ESTABLISHED", "RELATED", "INVALID")).Opt().D("With connection_state_type=CUSTOM")),
			P("create_allow_respond", Bool().Opt().D("For ALLOW policies: also create the return-traffic policy")),
			P("logging", Bool().Opt()),
			P("schedule", LooseObject(P("mode", Str())).Opt().D("e.g. {mode: 'ALWAYS'} (default) or a custom schedule object")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			stateType := a.Get("connection_state_type")
			if a.Truthy("connection_states") && !a.Truthy("connection_state_type") {
				stateType = "CUSTOM"
			}
			patch := unifi.Defined(
				"name", a.Get("name"),
				"description", a.Get("description"),
				"enabled", a.Get("enabled"),
				"action", a.Get("action"),
				"protocol", a.Get("protocol"),
				"ip_version", a.Get("ip_version"),
				"connection_state_type", stateType,
				"connection_states", a.Get("connection_states"),
				"create_allow_respond", a.Get("create_allow_respond"),
				"logging", a.Get("logging"),
				"schedule", a.Get("schedule"),
			)
			source, err := normalizeEndpoint(a.Obj("source"))
			if err != nil {
				return nil, err
			}
			destination, err := normalizeEndpoint(a.Obj("destination"))
			if err != nil {
				return nil, err
			}
			if id != "" {
				current, err := v2Get(ctx, client, s, "firewall-policies", id)
				if err != nil {
					return nil, err
				}
				if ojson.Truthy(current.Get("predefined")) {
					return nil, errorf("Predefined (system) firewall policies cannot be changed")
				}
				if source != nil {
					patch.Set("source", ojson.Spread(current.Get("source"), source))
				}
				if destination != nil {
					patch.Set("destination", ojson.Spread(current.Get("destination"), destination))
				}
				merged := ojson.Spread(current, patch, a.Get("extra"))
				e, err := enc(id)
				if err != nil {
					return nil, err
				}
				saved, err := client.V2(ctx, "PUT", s, "firewall-policies/"+e, merged)
				if err != nil {
					return nil, err
				}
				return summarizePolicy(savedOr(saved, merged)), nil
			}
			if !a.Truthy("name") || !a.Truthy("action") || source == nil || destination == nil {
				return nil, errorf("name, action, source and destination are required to create a firewall policy")
			}
			body := ojson.Spread(
				ojson.New(
					"enabled", true,
					"protocol", "all",
					"ip_version", "BOTH",
					"logging", false,
					"connection_state_type", "ALL",
					"schedule", ojson.New("mode", "ALWAYS"),
				),
				patch,
				ojson.New(
					"source", ojson.Spread(ojson.New("matching_target", "ANY"), source),
					"destination", ojson.Spread(ojson.New("matching_target", "ANY"), destination),
				),
				a.Get("extra"),
			)
			if !ojson.StrictEq(body.Get("action"), "ALLOW") {
				if ojson.Truthy(body.Get("create_allow_respond")) {
					return nil, errorf("create_allow_respond can only be used with ALLOW policies")
				}
				body.Set("create_allow_respond", false)
			}
			for _, side := range []string{"source", "destination"} {
				if !ojson.Truthy(ojson.Get(body.Get(side), "zone_id")) {
					return nil, fmt.Errorf("%s.zone_id is required (see unifi_list_firewall_zones)", side)
				}
			}
			created, err := client.V2(ctx, "POST", s, "firewall-policies", body)
			if err != nil {
				return nil, err
			}
			var result any
			if list, ok := ojson.Arr(created); ok {
				result = firstOf(list)
			} else {
				result = ojson.Coalesce(created, body)
			}
			return summarizePolicy(asObj(result)), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_firewall_policy", Title: "Delete firewall policy (zone-based)",
		Description: "Delete a user-defined zone-based firewall policy (predefined policies are refused).",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Firewall policy")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			current, err := v2Get(ctx, client, s, "firewall-policies", a.Str("id"))
			if err != nil {
				return nil, err
			}
			if ojson.Truthy(current.Get("predefined")) {
				return nil, errorf("Predefined (system) firewall policies cannot be deleted")
			}
			if err := v2Delete(ctx, client, s, "firewall-policies", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id"), "name", current.Get("name")), nil
		},
	})

	zones := func(ctx context.Context, a Args) (string, string, *Obj, *Obj, error) {
		s := c.site(a)
		siteID, err := integrationSiteID(ctx, client, s)
		if err != nil {
			return "", "", nil, nil, err
		}
		src, err := resolveIntegrationZone(ctx, client, s, siteID, a.Str("source_zone_id"))
		if err != nil {
			return "", "", nil, nil, err
		}
		dst, err := resolveIntegrationZone(ctx, client, s, siteID, a.Str("destination_zone_id"))
		if err != nil {
			return "", "", nil, nil, err
		}
		return s, siteID, src, dst, nil
	}
	orderingQuery := func(src, dst *Obj) []unifi.Param {
		return []unifi.Param{{Key: "sourceFirewallZoneId", Value: src.Get("id")}, {Key: "destinationFirewallZoneId", Value: dst.Get("id")}}
	}

	c.add(Def{
		Toolset: "firewall", Name: "unifi_get_firewall_policy_ordering", Title: "Get firewall policy order",
		Description: "Get the evaluation order of user-defined policies between a source and destination zone (beforeSystemDefined / afterSystemDefined policy ID lists). " + integrationNote,
		Input: props(
			siteArg(),
			P("source_zone_id", Str().D("Source zone ID (v2 _id from unifi_list_firewall_zones, or integration API id)")),
			P("destination_zone_id", Str().D("Destination zone ID")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			_, siteID, src, dst, err := zones(ctx, a)
			if err != nil {
				return nil, err
			}
			return client.Request(ctx, "GET", "/integration/v1/sites/"+siteID+"/firewall/policies/ordering", &unifi.Options{Body: U, Query: orderingQuery(src, dst)})
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_set_firewall_policy_ordering", Title: "Reorder firewall policies",
		Description: "Set the evaluation order of user-defined policies between two zones. Pass every policy ID from unifi_get_firewall_policy_ordering, reordered (IDs are the integration API's). " + integrationNote,
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("source_zone_id", Str().D("Source zone ID")),
			P("destination_zone_id", Str().D("Destination zone ID")),
			P("before_system_defined", Array(Str()).D("Policy IDs evaluated before the predefined policies, in order")),
			P("after_system_defined", Array(Str()).Opt().D("Policy IDs evaluated after the predefined policies, in order")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			_, siteID, src, dst, err := zones(ctx, a)
			if err != nil {
				return nil, err
			}
			path := "/integration/v1/sites/" + siteID + "/firewall/policies/ordering"
			query := orderingQuery(src, dst)
			current, err := client.Request(ctx, "GET", path, &unifi.Options{Body: U, Query: query})
			if err != nil {
				return nil, err
			}
			before := a.Arr("before_system_defined")
			after := a.Arr("after_system_defined")
			if after == nil {
				after = []any{}
			}
			ordered := ojson.New("beforeSystemDefined", before, "afterSystemDefined", after)
			have := append(
				append([]any{}, arr(ojson.Coalesce(ojson.Get(current, "orderedFirewallPolicyIds", "beforeSystemDefined"), ojson.Get(current, "beforeSystemDefined"), []any{}))...),
				arr(ojson.Coalesce(ojson.Get(current, "orderedFirewallPolicyIds", "afterSystemDefined"), ojson.Get(current, "afterSystemDefined"), []any{}))...,
			)
			want := append(append([]any{}, before...), after...)
			sortStrings(have)
			sortStrings(want)
			if len(have) > 0 {
				h, _ := ojson.Stringify(have)
				w, _ := ojson.Stringify(want)
				if h != w {
					return nil, fmt.Errorf("The new order must contain exactly the current policy IDs: %s", joinValues(have, ", "))
				}
			}
			return client.Request(ctx, "PUT", path, &unifi.Options{Query: query, Body: ojson.New("orderedFirewallPolicyIds", ordered)})
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_firewall_zones", Title: "List firewall zones",
		Description: "List zone-based firewall zones and the networks in each (UniFi Network 9.0+).",
		Input:       props(siteArg(), listing{}.args(), rawArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := v2List(ctx, client, c.site(a), "firewall/zone")
			if err != nil {
				return nil, err
			}
			return listing{}.result(a, list, func(z *Obj) any {
				return unifi.Compact(
					"id", z.Get("_id"),
					"name", z.Get("name"),
					"zone_key", z.Get("zone_key"),
					"system_defined", ojson.OrUndefined(z.Get("default_zone")),
					"network_ids", ojson.If(ojson.HasLength(z.Get("network_ids")), z.Get("network_ids"), U),
					"integration_id", z.Get("external_id"),
				)
			}, asIs), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_firewall_zone", Title: "Create or update firewall zone",
		Description: "Create a custom firewall zone (name required) or update one by id: rename it and/or set which networks it contains (network_ids replaces the list). System-defined zones cannot be renamed. " + integrationNote,
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("Firewall zone"),
			P("name", Str().MinLen(1).Opt()),
			P("network_ids", Array(Str()).Opt().D("Network IDs in the zone (see unifi_list_networks)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			siteID, err := integrationSiteID(ctx, client, s)
			if err != nil {
				return nil, err
			}
			if a.Str("id") == "" {
				if !a.Truthy("name") {
					return nil, errorf("name is required to create a firewall zone")
				}
				return client.Request(ctx, "POST", "/integration/v1/sites/"+siteID+"/firewall/zones", &unifi.Options{
					Body: ojson.New("name", a.Get("name"), "networkIds", ojson.Coalesce(a.Get("network_ids"), []any{})),
				})
			}
			zone, err := resolveIntegrationZone(ctx, client, s, siteID, a.Str("id"))
			if err != nil {
				return nil, err
			}
			systemDefined := ojson.StrictEq(ojson.Get(zone.Get("metadata"), "origin"), "SYSTEM_DEFINED")
			if systemDefined && a.Truthy("name") && !ojson.StrictEq(a.Get("name"), zone.Get("name")) {
				return nil, fmt.Errorf(`Cannot rename system-defined zone "%s"`, ojson.String(zone.Get("name")))
			}
			return client.Request(ctx, "PUT", "/integration/v1/sites/"+siteID+"/firewall/zones/"+ojson.String(zone.Get("id")), &unifi.Options{
				Body: ojson.New("name", ojson.Coalesce(a.Get("name"), zone.Get("name")), "networkIds", ojson.Coalesce(a.Get("network_ids"), zone.Get("networkIds"), []any{})),
			})
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_firewall_zone", Title: "Delete firewall zone",
		Description: "Delete a custom firewall zone (system-defined zones are refused). " + integrationNote,
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Firewall zone")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			siteID, err := integrationSiteID(ctx, client, s)
			if err != nil {
				return nil, err
			}
			zone, err := resolveIntegrationZone(ctx, client, s, siteID, a.Str("id"))
			if err != nil {
				return nil, err
			}
			if ojson.StrictEq(ojson.Get(zone.Get("metadata"), "origin"), "SYSTEM_DEFINED") {
				return nil, fmt.Errorf(`Cannot delete system-defined zone "%s"`, ojson.String(zone.Get("name")))
			}
			if _, err := client.Request(ctx, "DELETE", "/integration/v1/sites/"+siteID+"/firewall/zones/"+ojson.String(zone.Get("id")), nil); err != nil {
				return nil, err
			}
			return ojson.New("deleted", zone.Get("id"), "name", zone.Get("name")), nil
		},
	})

	// ---------------------------------------------------------------- traffic rules (v2)

	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_traffic_rules", Title: "List traffic rules",
		Description: "List traffic management rules (block/allow/limit by app, domain, IP, region or internet) from the v2 API.",
		Input:       props(siteArg(), listing{}.args(), rawArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			rules, err := v2List(ctx, client, c.site(a), "trafficrules")
			if err != nil {
				return nil, err
			}
			return listing{}.result(a, rules, func(r *Obj) any { return summarizeTrafficRule(r) }, redacted), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_traffic_rule", Title: "Create or update traffic rule",
		Description: "Create a traffic rule (action and matching_target required) or update one by id. Targets default to all clients on create. " +
			"matching_target picks what is matched: INTERNET (all traffic), DOMAIN (domains), APP (app_ids), APP_CATEGORY (app_category_ids), IP (ip_addresses), REGION (regions). " +
			"Setting a bandwidth limit makes it a rate-limiting rule.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("Traffic rule"),
			P("description", Str().Opt().D("Rule name/description")),
			P("enabled", Bool().Opt()),
			P("action", Enum("BLOCK", "ALLOW").Opt()),
			P("matching_target", Enum("INTERNET", "DOMAIN", "APP", "APP_CATEGORY", "IP", "REGION").Opt()),
			targetArgs(),
			P("domains", Array(Str()).Opt().D("Domains, e.g. ['example.com'] (matching_target=DOMAIN)")),
			P("ip_addresses", Array(Str()).Opt().D("IPs or CIDRs (matching_target=IP)")),
			P("app_ids", Array(Int()).Opt().D("DPI application IDs (matching_target=APP)")),
			P("app_category_ids", Array(Int()).Opt().D("DPI category IDs (matching_target=APP_CATEGORY)")),
			P("regions", Array(Str()).Opt().D("ISO country codes, e.g. ['CN','RU'] (matching_target=REGION)")),
			P("download_limit_kbps", Int().Positive().Opt().D("Download limit (enables the bandwidth limit)")),
			P("upload_limit_kbps", Int().Positive().Opt().D("Upload limit (enables the bandwidth limit)")),
			P("remove_bandwidth_limit", Bool().Opt().D("Disable the bandwidth limit")),
			P("schedule", LooseObject(P("mode", Str())).Opt().D("e.g. {mode:'ALWAYS'} (default) or {mode:'EVERY_DAY', time_range_start:'22:00', time_range_end:'06:00', ...}")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			targets, err := targetDevices(a)
			if err != nil {
				return nil, err
			}
			patch := unifi.Defined(
				"description", a.Get("description"),
				"enabled", a.Get("enabled"),
				"action", a.Get("action"),
				"matching_target", a.Get("matching_target"),
				"target_devices", targets,
				"domains", domainEntries(a.Arr("domains")),
				"ip_addresses", ipEntries(a.Arr("ip_addresses")),
				"app_ids", a.Get("app_ids"),
				"app_category_ids", a.Get("app_category_ids"),
				"regions", a.Get("regions"),
				"schedule", a.Get("schedule"),
			)
			var current *Obj
			if id != "" {
				if current, err = v2Get(ctx, client, s, "trafficrules", id); err != nil {
					return nil, err
				}
			}
			if a.Has("download_limit_kbps") || a.Has("upload_limit_kbps") {
				prev := ojson.Coalesce(current.Get("bandwidth_limit"), ojson.New())
				patch.Set("bandwidth_limit", ojson.Spread(prev, ojson.New(
					"enabled", true,
					"download_limit_kbps", ojson.Coalesce(a.Get("download_limit_kbps"), ojson.Get(prev, "download_limit_kbps"), 1024.0),
					"upload_limit_kbps", ojson.Coalesce(a.Get("upload_limit_kbps"), ojson.Get(prev, "upload_limit_kbps"), 1024.0),
				)))
			} else if a.Truthy("remove_bandwidth_limit") {
				prev := ojson.Coalesce(current.Get("bandwidth_limit"), ojson.New("download_limit_kbps", 1024.0, "upload_limit_kbps", 1024.0))
				patch.Set("bandwidth_limit", ojson.Spread(prev, ojson.New("enabled", false)))
			}

			if current != nil {
				merged := ojson.Spread(current, patch, a.Get("extra"))
				e, err := enc(id)
				if err != nil {
					return nil, err
				}
				saved, err := client.V2(ctx, "PUT", s, "trafficrules/"+e, merged)
				if err != nil {
					return nil, err
				}
				return summarizeTrafficRule(savedOr(saved, merged)), nil
			}
			if !a.Truthy("action") || !a.Truthy("matching_target") {
				return nil, errorf("action and matching_target are required to create a traffic rule")
			}
			body := ojson.Spread(ojson.New(
				"enabled", true,
				"description", ojson.Coalesce(a.Get("description"), ""),
				"app_category_ids", []any{},
				"app_ids", []any{},
				"domains", []any{},
				"ip_addresses", []any{},
				"ip_ranges", []any{},
				"network_ids", []any{},
				"regions", []any{},
				"target_devices", []any{ojson.New("type", "ALL_CLIENTS")},
				"bandwidth_limit", ojson.New("enabled", false, "download_limit_kbps", 1024.0, "upload_limit_kbps", 1024.0),
				"schedule", ojson.New("mode", "ALWAYS", "repeat_on_days", []any{}, "time_all_day", false),
			), patch, a.Get("extra"))
			saved, err := v2Save(ctx, client, s, "trafficrules", "", body)
			if err != nil {
				return nil, err
			}
			return summarizeTrafficRule(asObj(saved)), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_traffic_rule", Title: "Delete traffic rule",
		Description: "Delete a traffic management rule.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Traffic rule")),
		Handler:     deleteHandler(c, "trafficrules", true),
	})

	registerPolicyTable(c)
}

// deleteHandler deletes a rest (or v2) object by id and returns {deleted: id}.
func deleteHandler(c *Context, collection string, v2 bool) func(context.Context, Args) (any, error) {
	return func(ctx context.Context, a Args) (any, error) {
		var err error
		if v2 {
			err = v2Delete(ctx, c.Client, c.site(a), collection, a.Str("id"))
		} else {
			err = restDelete(ctx, c.Client, c.site(a), collection, a.Str("id"))
		}
		if err != nil {
			return nil, err
		}
		return ojson.New("deleted", a.Get("id")), nil
	}
}

// sortStrings is Array.prototype.sort() with no comparator: by string value, in UTF-16 code unit order.
func sortStrings(list []any) {
	sort.SliceStable(list, func(i, j int) bool {
		if ojson.IsUndefined(list[i]) {
			return false
		}
		if ojson.IsUndefined(list[j]) {
			return true
		}
		return ojson.String(list[i]) < ojson.String(list[j])
	})
}
