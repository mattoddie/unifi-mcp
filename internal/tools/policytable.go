package tools

// Policy table objects on the v2 API that sit next to the zone-based firewall:
// QoS rules, NAT rules, MAC ACL rules and object-oriented network (OON) policies.
// They are registered in the firewall toolset (see registerFirewall).

import (
	"context"
	"math"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// OON policies list on a plural path but are created, updated and deleted on a singular one.
const (
	oonList   = "object-oriented-network-configs"
	oonSingle = "object-oriented-network-config"
)

// nextIndex is one more than the largest numeric field among items (1 when there are none),
// skipping items for which skip returns true.
func nextIndex(items []any, field string, skip func(any) bool) float64 {
	highest := 0.0
	for _, it := range items {
		if skip != nil && skip(it) {
			continue
		}
		if n, ok := ojson.Num(ojson.Get(it, field)); ok {
			highest = math.Max(highest, n)
		}
	}
	return highest + 1
}

// mergeInto returns current[key] with patch spread over it (one level deep).
func mergeInto(current *Obj, key string, patch *Obj) *Obj {
	var prev any = U
	if current != nil {
		prev = current.Get(key)
	}
	if _, ok := ojson.AsObj(prev); !ok {
		prev = ojson.New()
	}
	return ojson.Spread(prev, patch)
}

// listPolicyObjects lists a v2 collection through a listing; raw objects are redacted.
func listPolicyObjects(ctx context.Context, c *Context, a Args, l listing, collection string, summarize func(*Obj) *Obj) (any, error) {
	items, err := v2List(ctx, c.Client, c.site(a), collection)
	if err != nil {
		return nil, err
	}
	return l.result(a, items, func(o *Obj) any { return summarize(o) }, redacted), nil
}

// putV2 PUTs a full merged object to <path>/<id> and returns the saved object.
func putV2(ctx context.Context, c *Context, site, path, id string, merged *Obj) (*Obj, error) {
	e, err := enc(id)
	if err != nil {
		return nil, err
	}
	saved, err := c.Client.V2(ctx, "PUT", site, path+"/"+e, merged)
	if err != nil {
		return nil, err
	}
	return savedOr(first(saved), merged), nil
}

// ---------------------------------------------------------------- summaries

func summarizeQosRule(r *Obj) *Obj {
	g := r.Get
	src, dst := g("source"), g("destination")
	nonEmpty := func(v any) any { return ojson.If(ojson.HasLength(v), v, U) }
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"enabled", g("enabled"),
		"index", g("index"),
		"objective", g("objective"),
		"download_limit_kbps", g("download_limit_kbps"),
		"upload_limit_kbps", g("upload_limit_kbps"),
		"download_burst", ojson.If(ojson.StrictEq(g("download_burst"), "OFF"), U, g("download_burst")),
		"upload_burst", ojson.If(ojson.StrictEq(g("upload_burst"), "OFF"), U, g("upload_burst")),
		"source", unifi.Compact(
			"matching_target", ojson.Get(src, "matching_target"),
			"client_macs", nonEmpty(ojson.Get(src, "client_macs")),
			"network_ids", nonEmpty(ojson.Get(src, "network_ids")),
		),
		"destination", unifi.Compact(
			"matching_target", ojson.Get(dst, "matching_target"),
			"app_ids", nonEmpty(ojson.Get(dst, "app_ids")),
			"app_category_ids", nonEmpty(ojson.Get(dst, "app_category_ids")),
			"ips", nonEmpty(ojson.Get(dst, "ips")),
			"regions", nonEmpty(ojson.Get(dst, "regions")),
			"web_domains", nonEmpty(ojson.Get(dst, "web_domains")),
			"port", ojson.If(ojson.Truthy(ojson.Get(dst, "port")), ojson.Get(dst, "port"), U),
		),
		"schedule", nonDefaultSchedule(g("schedule")),
	)
}

func summarizeNatFilter(f any) any {
	t := ojson.Get(f, "filter_type")
	if !ojson.Truthy(t) || ojson.StrictEq(t, "NONE") {
		return U
	}
	return unifi.Compact(
		"type", t,
		"address", ojson.Get(f, "address"),
		"port", ojson.Get(f, "port"),
		"network_id", ojson.Get(f, "network_conf_id"),
		"firewall_group_ids", ojson.If(ojson.HasLength(ojson.Get(f, "firewall_group_ids")), ojson.Get(f, "firewall_group_ids"), U),
		"invert_address", ojson.OrUndefined(ojson.Get(f, "invert_address")),
		"invert_port", ojson.OrUndefined(ojson.Get(f, "invert_port")),
	)
}

func summarizeNatRule(r *Obj) *Obj {
	g := r.Get
	return unifi.Compact(
		"id", g("_id"),
		"description", g("description"),
		"enabled", g("enabled"),
		"type", g("type"),
		"rule_index", g("rule_index"),
		"ip_version", g("ip_version"),
		"protocol", g("protocol"),
		"in_interface", g("in_interface"),
		"out_interface", g("out_interface"),
		"translated_ip", g("ip_address"),
		"translated_port", g("port"),
		"source", summarizeNatFilter(g("source_filter")),
		"destination", summarizeNatFilter(g("destination_filter")),
		"exclude", ojson.OrUndefined(g("exclude")),
		"logging", ojson.OrUndefined(g("logging")),
		"predefined", ojson.OrUndefined(g("is_predefined")),
	)
}

func summarizeAclRule(r *Obj) *Obj {
	g := r.Get
	side := func(v any) any {
		return unifi.Compact(
			"macs", ojson.If(ojson.HasLength(ojson.Get(v, "specific_mac_addresses")), ojson.Get(v, "specific_mac_addresses"), U),
			"mac_mask", ojson.Get(v, "mac_mask"),
		)
	}
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"enabled", g("enabled"),
		"type", g("type"),
		"action", g("action"),
		"acl_index", g("acl_index"),
		"network_id", g("mac_acl_network_id"),
		"source", side(g("traffic_source")),
		"destination", side(g("traffic_destination")),
	)
}

func summarizeOonPolicy(p *Obj) *Obj {
	g := p.Get
	var targets any = U
	if list, ok := ojson.Arr(g("targets")); ok && len(list) > 0 {
		targets = ojson.Map(list, func(t any) any {
			if _, isStr := t.(string); isStr {
				return t
			}
			return ojson.Coalesce(ojson.Get(t, "value"), ojson.Get(t, "id"), t)
		})
	}
	section := func(k string) any {
		v := g(k)
		if !ojson.Truthy(ojson.Get(v, "enabled")) {
			return U
		}
		return unifi.RedactSecrets(v)
	}
	return unifi.Compact(
		"id", ojson.Coalesce(g("_id"), g("id")),
		"name", g("name"),
		"enabled", g("enabled"),
		"target_type", g("target_type"),
		"targets", targets,
		"secure", section("secure"),
		"qos", section("qos"),
		"route", section("route"),
	)
}

// ---------------------------------------------------------------- input helpers

func qosSource(a Args) (*Obj, error) {
	clientMacs, networkIDs := a.Arr("client_macs"), a.Arr("network_ids")
	switch {
	case clientMacs != nil && networkIDs != nil:
		return nil, errorf("Pass client_macs or network_ids, not both")
	case clientMacs != nil:
		list, err := macs(clientMacs)
		if err != nil {
			return nil, err
		}
		return ojson.New("matching_target", "CLIENT", "client_macs", list, "network_ids", []any{}), nil
	case networkIDs != nil:
		return ojson.New("matching_target", "NETWORK", "client_macs", []any{}, "network_ids", networkIDs), nil
	case a.Truthy("all_clients"):
		return ojson.New("matching_target", "ANY", "client_macs", []any{}, "network_ids", []any{}), nil
	}
	return nil, nil
}

func qosDestination(a Args) (*Obj, error) {
	kinds := [][2]string{{"app_ids", "APP"}, {"app_category_ids", "APP_CATEGORY"}, {"ips", "IP"}, {"regions", "REGION"}, {"web_domains", "WEB"}}
	var dst *Obj
	for _, k := range kinds {
		if !a.Has(k[0]) {
			continue
		}
		if dst != nil {
			return nil, errorf("Pass only one of app_ids, app_category_ids, ips, regions or web_domains")
		}
		dst = ojson.New("matching_target", k[1], "matching_target_type", "SPECIFIC")
		for _, other := range kinds {
			dst.Set(other[0], []any{})
		}
		dst.Set(k[0], a.Get(k[0]))
	}
	if dst == nil && a.Truthy("any_destination") {
		dst = ojson.New("matching_target", "ANY", "app_ids", []any{}, "app_category_ids", []any{}, "ips", []any{}, "regions", []any{}, "web_domains", []any{})
	}
	if a.Has("destination_port") {
		if dst == nil {
			dst = ojson.New()
		}
		if a.Str("destination_port") == "" {
			dst.Set("port_matching_type", "ANY").Set("port", "")
		} else {
			dst.Set("port_matching_type", "SPECIFIC").Set("port", a.Get("destination_port"))
		}
	}
	return dst, nil
}

func natFilter(a Args, prefix string) *Obj {
	addr, port := a.Get(prefix+"_address"), a.Get(prefix+"_port")
	if ojson.IsUndefined(addr) && ojson.IsUndefined(port) {
		return nil
	}
	if ojson.StrictEq(addr, "") && ojson.StrictEq(port, "") {
		return ojson.New("filter_type", "NONE", "address", "", "port", "")
	}
	return unifi.Defined("filter_type", "ADDRESS_AND_PORT", "address", addr, "port", port)
}

func defaultNatFilter() *Obj {
	return ojson.New("filter_type", "NONE", "firewall_group_ids", []any{}, "invert_address", false, "invert_port", false)
}

// checkNat applies the per-type requirements the Network UI enforces.
func checkNat(r *Obj) error {
	has := func(k string) bool { return ojson.Truthy(r.Get(k)) }
	switch r.Get("type") {
	case "DNAT":
		if !has("in_interface") || !has("ip_address") {
			return errorf("A DNAT rule needs in_interface and translated_ip")
		}
		if t := ojson.Get(r.Get("destination_filter"), "filter_type"); !ojson.Truthy(t) || ojson.StrictEq(t, "NONE") {
			return errorf("A DNAT rule needs a destination to match: pass destination_address and/or destination_port")
		}
	case "SNAT":
		if !has("out_interface") || !has("ip_address") {
			return errorf("An SNAT rule needs out_interface and translated_ip")
		}
	case "MASQUERADE":
		if !has("out_interface") {
			return errorf("A MASQUERADE rule needs out_interface")
		}
		if has("ip_address") || has("port") {
			return errorf("A MASQUERADE rule translates to the interface address, so translated_ip and translated_port must be empty")
		}
	}
	return nil
}

func oonTargets(targetType any, list []any) ([]any, error) {
	out := make([]any, len(list))
	for i, t := range list {
		if ojson.StrictEq(targetType, "CLIENTS") {
			m, err := mac(t)
			if err != nil {
				return nil, err
			}
			out[i] = ojson.New("type", "MAC", "value", m)
		} else {
			out[i] = ojson.New("type", "NETWORK_GROUP_ID", "value", t)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- tools

func registerPolicyTable(c *Context) {
	client := c.Client

	// ---------------------------------------------------------------- QoS rules

	qosList := listing{Search: []string{"name", "objective", "id", "source.client_macs", "destination.web_domains"}}
	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_qos_rules", Title: "List QoS rules",
		Description: "List QoS rules from the policy table (recent Network 9.x/10.x with a UniFi gateway): objective (LIMIT, PRIORITIZE or LIMIT_AND_PRIORITIZE), rate limits in kbps, the clients/networks they apply to and the apps, IPs, regions or domains they match.",
		Input:       props(siteArg(), qosList.args(), rawArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			return listPolicyObjects(ctx, c, a, qosList, "qos-rules", summarizeQosRule)
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_qos_rule", Title: "Create or update QoS rule",
		Description: "Create a QoS rule (name and objective required) or update one by id; only the fields passed change. " +
			"LIMIT caps bandwidth (kbps), PRIORITIZE moves matching traffic to a higher-priority queue, LIMIT_AND_PRIORITIZE does both. " +
			"Source defaults to all clients and destination to any traffic on create. A rule with a low limit can make the matched traffic unusable. " +
			"Recent Network 9.x/10.x with a UniFi gateway; field names were taken from Network 10.6, so use `extra` for anything else.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("QoS rule"),
			P("name", Str().MinLen(1).Opt().D("Rule name (required when creating)")),
			P("enabled", Bool().Opt().D("Enable or disable the rule (default true on create)")),
			P("objective", Enum("LIMIT", "PRIORITIZE", "LIMIT_AND_PRIORITIZE").Opt().D("What the rule does (required when creating)")),
			P("download_limit_kbps", Int().Positive().Opt().D("Download limit in kbps (LIMIT objectives)")),
			P("upload_limit_kbps", Int().Positive().Opt().D("Upload limit in kbps (LIMIT objectives)")),
			P("download_burst", Enum("OFF", "SHORT", "LONG").Opt().D("Allow short or long bursts above the download limit")),
			P("upload_burst", Enum("OFF", "SHORT", "LONG").Opt().D("Allow short or long bursts above the upload limit")),
			P("all_clients", Bool().Opt().D("Apply to all clients (the default on create)")),
			P("client_macs", Array(Str()).Opt().D("Apply to these client MAC addresses")),
			P("network_ids", Array(Str()).Opt().D("Apply to clients on these network IDs (see unifi_list_networks)")),
			P("any_destination", Bool().Opt().D("Match all traffic (the default on create)")),
			P("app_ids", Array(Int()).Opt().D("Match these DPI application IDs")),
			P("app_category_ids", Array(Int()).Opt().D("Match these DPI category IDs")),
			P("ips", Array(Str()).Opt().D("Match these destination IPs or CIDRs")),
			P("regions", Array(Str()).Opt().D("Match these ISO country codes, e.g. ['US']")),
			P("web_domains", Array(Str()).Opt().D("Match these domains, e.g. ['example.com']")),
			P("destination_port", Str().Opt().D("Match this destination port or range, e.g. '443'; empty string matches any port")),
			P("wan_or_vpn_network", Str().Opt().D("WAN or VPN network ID the rule applies to (empty for all)")),
			P("schedule", LooseObject(P("mode", Str())).Opt().D("e.g. {mode:'ALWAYS'} (default) or {mode:'EVERY_DAY', time_range_start:'22:00', time_range_end:'06:00'}")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			src, err := qosSource(a)
			if err != nil {
				return nil, err
			}
			dst, err := qosDestination(a)
			if err != nil {
				return nil, err
			}
			patch := unifi.Defined(
				"name", a.Get("name"),
				"enabled", a.Get("enabled"),
				"objective", a.Get("objective"),
				"download_limit_kbps", a.Get("download_limit_kbps"),
				"upload_limit_kbps", a.Get("upload_limit_kbps"),
				"download_burst", a.Get("download_burst"),
				"upload_burst", a.Get("upload_burst"),
				"wan_or_vpn_network", a.Get("wan_or_vpn_network"),
				"schedule", a.Get("schedule"),
			)
			if id != "" {
				current, err := v2Get(ctx, client, s, "qos-rules", id)
				if err != nil {
					return nil, err
				}
				if src != nil {
					patch.Set("source", mergeInto(current, "source", src))
				}
				if dst != nil {
					patch.Set("destination", mergeInto(current, "destination", dst))
				}
				patch.Assign(a.Get("extra"))
				if patch.Len() == 0 {
					return nil, errorf("Nothing to update")
				}
				saved, err := putV2(ctx, c, s, "qos-rules", id, ojson.Spread(current, patch))
				if err != nil {
					return nil, err
				}
				return summarizeQosRule(saved), nil
			}
			if !a.Truthy("name") || !a.Truthy("objective") {
				return nil, errorf("name and objective are required to create a QoS rule")
			}
			if !ojson.StrictEq(a.Get("objective"), "PRIORITIZE") && !a.Has("download_limit_kbps") && !a.Has("upload_limit_kbps") {
				return nil, errorf("A LIMIT rule needs download_limit_kbps and/or upload_limit_kbps")
			}
			source := ojson.New("matching_target", "ANY", "client_macs", []any{}, "network_ids", []any{}, "port_matching_type", "ANY")
			destination := ojson.New("matching_target", "ANY", "matching_target_type", "SPECIFIC",
				"app_ids", []any{}, "app_category_ids", []any{}, "ips", []any{}, "regions", []any{}, "web_domains", []any{},
				"port_matching_type", "ANY")
			body := ojson.Spread(ojson.New(
				"enabled", true,
				"download_burst", "OFF",
				"upload_burst", "OFF",
				"source", ojson.Spread(source, src),
				"destination", ojson.Spread(destination, dst),
				"schedule", ojson.New("mode", "ALWAYS", "repeat_on_days", []any{}, "time_all_day", false),
			), patch, a.Get("extra"))
			saved, err := v2Create(ctx, client, s, "qos-rules", body)
			if err != nil {
				return nil, err
			}
			return summarizeQosRule(savedOr(first(saved), body)), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_qos_rule", Title: "Delete QoS rule",
		Description: "Delete a QoS rule.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("QoS rule")),
		Handler:     deleteHandler(c, "qos-rules", true),
	})

	// ---------------------------------------------------------------- NAT rules

	natList := listing{Search: []string{"description", "type", "ip_address", "source_filter.address", "destination_filter.address"}}
	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_nat_rules", Title: "List NAT rules",
		Description: "List custom NAT rules (DNAT, SNAT and masquerade) from the v2 API, including the predefined ones. Port forwards are separate (unifi_list_port_forwards). Needs Network 9.0+ with a UniFi gateway and the zone-based firewall.",
		Input:       props(siteArg(), natList.args(), rawArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			return listPolicyObjects(ctx, c, a, natList, "nat", summarizeNatRule)
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_nat_rule", Title: "Create or update NAT rule",
		Description: "Create a NAT rule (type required) or update one by id; only the fields passed change. " +
			"DNAT rewrites the destination of traffic arriving on in_interface (needs translated_ip and a destination to match); " +
			"SNAT rewrites the source of traffic leaving out_interface (needs translated_ip); MASQUERADE uses the out_interface address. " +
			"A wrong NAT rule can break internet access or expose internal hosts. Copy interface values from an existing rule (unifi_list_nat_rules with raw) if unsure. " +
			"Network 9.0+ with a UniFi gateway and the zone-based firewall.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("NAT rule"),
			P("description", Str().Opt().D("Rule name/description")),
			P("enabled", Bool().Opt().D("Enable or disable the rule (default true on create)")),
			P("type", Enum("DNAT", "SNAT", "MASQUERADE").Opt().D("NAT type (required when creating)")),
			P("protocol", Enum("all", "tcp", "udp", "tcp_udp").Opt().D("Protocol to match (default all)")),
			P("ip_version", Enum("IPV4", "IPV6").Opt().D("IP version (default IPV4)")),
			P("in_interface", Str().Opt().D("Interface (network ID) the traffic arrives on, for DNAT")),
			P("out_interface", Str().Opt().D("Interface (network ID) the traffic leaves on, for SNAT and MASQUERADE")),
			P("translated_ip", Str().Opt().D("Address to translate to (DNAT destination or SNAT source)")),
			P("translated_port", Str().Opt().D("Port to translate to, e.g. '8080' (empty string clears it)")),
			P("source_address", Str().Opt().D("Match this source IP or CIDR (empty strings for address and port match any source)")),
			P("source_port", Str().Opt().D("Match this source port")),
			P("destination_address", Str().Opt().D("Match this destination IP or CIDR")),
			P("destination_port", Str().Opt().D("Match this destination port")),
			P("rule_index", Int().Min(0).Opt().D("Position of the rule (default: after the last custom rule)")),
			P("exclude", Bool().Opt().D("Exclude matching traffic from NAT instead of translating it")),
			P("logging", Bool().Opt().D("Log matching traffic")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			patch := unifi.Defined(
				"description", a.Get("description"),
				"enabled", a.Get("enabled"),
				"type", a.Get("type"),
				"protocol", a.Get("protocol"),
				"ip_version", a.Get("ip_version"),
				"in_interface", a.Get("in_interface"),
				"out_interface", a.Get("out_interface"),
				"ip_address", a.Get("translated_ip"),
				"port", a.Get("translated_port"),
				"rule_index", a.Get("rule_index"),
				"exclude", a.Get("exclude"),
				"logging", a.Get("logging"),
			)
			srcFilter, dstFilter := natFilter(a, "source"), natFilter(a, "destination")
			if id != "" {
				current, err := v2Get(ctx, client, s, "nat", id)
				if err != nil {
					return nil, err
				}
				if ojson.Truthy(current.Get("is_predefined")) {
					return nil, errorf("This is a predefined NAT rule and can't be changed")
				}
				if srcFilter != nil {
					patch.Set("source_filter", mergeInto(current, "source_filter", srcFilter))
				}
				if dstFilter != nil {
					patch.Set("destination_filter", mergeInto(current, "destination_filter", dstFilter))
				}
				patch.Assign(a.Get("extra"))
				if patch.Len() == 0 {
					return nil, errorf("Nothing to update")
				}
				merged := ojson.Spread(current, patch)
				if err := checkNat(merged); err != nil {
					return nil, err
				}
				saved, err := putV2(ctx, c, s, "nat", id, merged)
				if err != nil {
					return nil, err
				}
				return summarizeNatRule(saved), nil
			}
			if !a.Truthy("type") {
				return nil, errorf("type is required to create a NAT rule")
			}
			if !a.Has("rule_index") {
				existing, err := v2List(ctx, client, s, "nat")
				if err != nil {
					return nil, err
				}
				patch.Set("rule_index", nextIndex(existing, "rule_index", func(r any) bool { return ojson.Truthy(ojson.Get(r, "is_predefined")) }))
			}
			body := ojson.Spread(ojson.New(
				"description", "",
				"enabled", true,
				"ip_version", "IPV4",
				"protocol", "all",
				"logging", false,
				"exclude", false,
				"pppoe_use_base_interface", false,
				"is_predefined", false,
				"setting_preference", "manual",
				"source_filter", ojson.Spread(defaultNatFilter(), srcFilter),
				"destination_filter", ojson.Spread(defaultNatFilter(), dstFilter),
			), patch, a.Get("extra"))
			if err := checkNat(body); err != nil {
				return nil, err
			}
			saved, err := v2Create(ctx, client, s, "nat", body)
			if err != nil {
				return nil, err
			}
			return summarizeNatRule(savedOr(first(saved), body)), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_nat_rule", Title: "Delete NAT rule",
		Description: "Delete a custom NAT rule. Predefined rules can't be deleted.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("NAT rule")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			current, err := v2Get(ctx, client, s, "nat", a.Str("id"))
			if err != nil {
				return nil, err
			}
			if ojson.Truthy(current.Get("is_predefined")) {
				return nil, errorf("This is a predefined NAT rule and can't be deleted")
			}
			if err := v2Delete(ctx, client, s, "nat", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id")), nil
		},
	})

	// ---------------------------------------------------------------- ACL rules

	aclList := listing{
		Search:  []string{"name", "action", "traffic_source.specific_mac_addresses", "traffic_destination.specific_mac_addresses"},
		Filters: map[string]string{"network_id": "network_id"},
	}
	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_acl_rules", Title: "List ACL rules",
		Description: "List switch ACL rules (MAC ACLs that allow or block traffic between devices inside one network, enforced by UniFi switches). Rules are processed in acl_index order. Needs Network 9.4+.",
		Input: props(
			siteArg(),
			P("network_id", Str().Opt().D("Only rules for this network ID")),
			aclList.args(), rawArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			items, err := v2List(ctx, client, c.site(a), "acl-rules")
			if err != nil {
				return nil, err
			}
			ojson.SortStable(items, func(x, y any) float64 {
				return ojson.ToNumber(ojson.Coalesce(ojson.Get(x, "acl_index"), 0.0)) - ojson.ToNumber(ojson.Coalesce(ojson.Get(y, "acl_index"), 0.0))
			})
			return aclList.result(a, items, func(o *Obj) any { return summarizeAclRule(o) }, redacted), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_acl_rule", Title: "Create or update ACL rule",
		Description: "Create a MAC ACL rule (name, action and network_id required) or update one by id; only the fields passed change. " +
			"BLOCK stops matching devices on the network from talking to each other at the switch, which can cut devices off. " +
			"New rules go after the existing ones unless acl_index is given. Needs Network 9.4+; use `extra` for fields such as mac_mask.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("ACL rule"),
			P("name", Str().MinLen(1).Opt().D("Rule name (required when creating)")),
			P("enabled", Bool().Opt().D("Enable or disable the rule (default true on create)")),
			P("action", Enum("ALLOW", "BLOCK").Opt().D("ALLOW or BLOCK (required when creating)")),
			P("network_id", Str().Opt().D("Network ID the rule applies in (required when creating; see unifi_list_networks)")),
			P("source_macs", Array(Str()).Opt().D("Source device MAC addresses (replaces the list)")),
			P("destination_macs", Array(Str()).Opt().D("Destination device MAC addresses (replaces the list)")),
			P("acl_index", Int().Min(0).Opt().D("Position in the rule order (lower runs first)")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			patch := unifi.Defined(
				"name", a.Get("name"),
				"enabled", a.Get("enabled"),
				"action", a.Get("action"),
				"mac_acl_network_id", a.Get("network_id"),
				"acl_index", a.Get("acl_index"),
			)
			side := func(k string) (*Obj, error) {
				if !a.Has(k) {
					return nil, nil
				}
				list, err := macs(a.Arr(k))
				if err != nil {
					return nil, err
				}
				return ojson.New("type", "CLIENT_MAC", "specific_mac_addresses", list), nil
			}
			src, err := side("source_macs")
			if err != nil {
				return nil, err
			}
			dst, err := side("destination_macs")
			if err != nil {
				return nil, err
			}
			if id != "" {
				current, err := v2Get(ctx, client, s, "acl-rules", id)
				if err != nil {
					return nil, err
				}
				if src != nil {
					patch.Set("traffic_source", mergeInto(current, "traffic_source", src))
				}
				if dst != nil {
					patch.Set("traffic_destination", mergeInto(current, "traffic_destination", dst))
				}
				patch.Assign(a.Get("extra"))
				if patch.Len() == 0 {
					return nil, errorf("Nothing to update")
				}
				saved, err := putV2(ctx, c, s, "acl-rules", id, ojson.Spread(current, patch))
				if err != nil {
					return nil, err
				}
				return summarizeAclRule(saved), nil
			}
			if !a.Truthy("name") || !a.Truthy("action") || !a.Truthy("network_id") {
				return nil, errorf("name, action and network_id are required to create an ACL rule")
			}
			existing, err := v2List(ctx, client, s, "acl-rules")
			if err != nil {
				return nil, err
			}
			if !a.Has("acl_index") {
				patch.Set("acl_index", nextIndex(existing, "acl_index", nil))
			}
			emptySide := func() *Obj {
				return ojson.New("type", "CLIENT_MAC", "specific_mac_addresses", []any{}, "ips_or_subnets", []any{}, "network_ids", []any{}, "ports", []any{})
			}
			body := ojson.Spread(ojson.New(
				"type", "MAC",
				"enabled", true,
				"specific_enforcers", []any{},
				"traffic_source", ojson.Spread(emptySide(), src),
				"traffic_destination", ojson.Spread(emptySide(), dst),
			), patch, a.Get("extra"))
			saved, err := v2Create(ctx, client, s, "acl-rules", body)
			if err != nil {
				return nil, err
			}
			result, ok := ojson.AsObj(first(saved))
			if !ok || !ojson.Truthy(result.Get("_id")) {
				// Some versions answer an empty body; read the rule back by name.
				if all, err := v2List(ctx, client, s, "acl-rules"); err == nil {
					if found, ok := ojson.AsObj(ojson.Find(all, func(r any) bool { return ojson.StrictEq(ojson.Get(r, "name"), body.Get("name")) })); ok {
						result = found
					}
				}
			}
			return summarizeAclRule(savedOr(result, body)), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_acl_rule", Title: "Delete ACL rule",
		Description: "Delete a switch ACL rule.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("ACL rule")),
		Handler:     deleteHandler(c, "acl-rules", true),
	})

	// ---------------------------------------------------------------- object-oriented network policies

	oonPolicies := listing{Search: []string{"name", "target_type", "targets", "targets.value"}}
	c.add(Def{
		Toolset: "firewall", Name: "unifi_list_oon_policies", Title: "List object-oriented network policies",
		Description: "List object-oriented networking (OON) policies: rules attached to clients or client groups that schedule or block internet access and apps (secure), limit bandwidth (qos) or route through a VPN (route). Needs Network 9.4+ with a UniFi gateway.",
		Input:       props(siteArg(), oonPolicies.args(), rawArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			return listPolicyObjects(ctx, c, a, oonPolicies, oonList, summarizeOonPolicy)
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_save_oon_policy", Title: "Create or update object-oriented network policy",
		Description: "Create an OON policy (name, target_type, targets and at least one of secure, qos or route) or update one by id; only the fields passed change, and secure, qos and route are merged into the current values one level deep. " +
			"The section shapes vary by Network version: read an existing policy with unifi_list_oon_policies raw=true and copy its structure, e.g. secure: {enabled:true, internet:{mode:'TURN_OFF_INTERNET', schedule:{...}}}. " +
			"A policy can cut targets off the internet. Needs Network 9.4+ with a UniFi gateway.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			saveIDArg("OON policy"),
			P("name", Str().MinLen(1).Opt().D("Policy name (required when creating)")),
			P("enabled", Bool().Opt().D("Enable or disable the policy (default true on create)")),
			P("target_type", Enum("CLIENTS", "GROUPS").Opt().D("CLIENTS (targets are MAC addresses) or GROUPS (targets are client group IDs); required when creating")),
			P("targets", Array(Str()).Opt().D("Client MACs or group IDs, depending on target_type (replaces the list)")),
			P("secure", Record(nil).Opt().D("Internet access and app blocking settings (controller field names)")),
			P("qos", Record(nil).Opt().D("Bandwidth limit settings (controller field names)")),
			P("route", Record(nil).Opt().D("VPN routing settings (controller field names)")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			var current *Obj
			if id != "" {
				var err error
				if current, err = v2Get(ctx, client, s, oonList, id); err != nil {
					return nil, err
				}
			}
			patch := unifi.Defined("name", a.Get("name"), "enabled", a.Get("enabled"), "target_type", a.Get("target_type"))
			if a.Has("targets") {
				targetType := a.Get("target_type")
				if ojson.IsUndefined(targetType) && current != nil {
					targetType = current.Get("target_type")
				}
				targets, err := oonTargets(targetType, a.Arr("targets"))
				if err != nil {
					return nil, err
				}
				patch.Set("targets", targets)
			} else if current != nil && a.Has("target_type") && !ojson.StrictEq(a.Get("target_type"), current.Get("target_type")) {
				return nil, errorf("Pass targets when changing target_type")
			}
			for _, k := range []string{"secure", "qos", "route"} {
				if o := a.Obj(k); o != nil {
					patch.Set(k, mergeInto(current, k, o))
				}
			}
			patch.Assign(a.Get("extra"))
			if current != nil {
				if patch.Len() == 0 {
					return nil, errorf("Nothing to update")
				}
				saved, err := putV2(ctx, c, s, oonSingle, id, ojson.Spread(current, patch))
				if err != nil {
					return nil, err
				}
				return summarizeOonPolicy(saved), nil
			}
			if !a.Truthy("name") || !a.Truthy("target_type") || !a.Has("targets") {
				return nil, errorf("name, target_type and targets are required to create an OON policy")
			}
			if a.Obj("secure") == nil && a.Obj("qos") == nil && a.Obj("route") == nil {
				return nil, errorf("Configure at least one of secure, qos or route")
			}
			body := ojson.Spread(ojson.New("enabled", true), patch)
			saved, err := v2Create(ctx, client, s, oonSingle, body)
			if err != nil {
				return nil, err
			}
			return summarizeOonPolicy(savedOr(first(saved), body)), nil
		},
	})

	c.add(Def{
		Toolset: "firewall", Name: "unifi_delete_oon_policy", Title: "Delete object-oriented network policy",
		Description: "Delete an object-oriented network policy.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("OON policy")),
		Handler:     deleteHandler(c, oonSingle, true),
	})
}
