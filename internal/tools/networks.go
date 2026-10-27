package tools

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// lanPurposes are the purposes the LAN tools manage; WANs and VPNs have their own tools.
var lanPurposes = ojson.Strings([]string{"corporate", "guest", "vlan-only"})

// SubnetRange is a subnet's gateway and its first and last DHCP host.
type SubnetRange struct{ Gateway, First, Last string }

var subnetPattern = regexp.MustCompile(`^(\d{1,3}(?:\.\d{1,3}){3})/(\d{1,2})$`)

// subnetRange parses "192.168.10.1/24" into the gateway address and the first/last usable host.
func subnetRange(subnet string) (SubnetRange, error) {
	m := subnetPattern.FindStringSubmatch(subnet)
	if m == nil {
		return SubnetRange{}, fmt.Errorf(`subnet must be in gateway/prefix form, e.g. 192.168.10.1/24 (got "%s")`, subnet)
	}
	prefix, _ := strconv.Atoi(m[2])
	var ip float64
	bad := false
	for _, o := range strings.Split(m[1], ".") {
		n, _ := strconv.Atoi(o)
		if n > 255 {
			bad = true
		}
		ip = ip*256 + float64(n)
	}
	if bad || prefix < 8 || prefix > 30 {
		return SubnetRange{}, fmt.Errorf(`Invalid subnet "%s" (prefix must be /8-/30)`, subnet)
	}
	size := math.Pow(2, float64(32-prefix))
	network := math.Floor(ip/size) * size
	broadcast := network + size - 1
	if ip == network || ip == broadcast {
		return SubnetRange{}, fmt.Errorf(`"%s": the address must be the gateway IP, not the network or broadcast address`, subnet)
	}
	toIP := func(n float64) string {
		parts := make([]string, 4)
		for i, s := range []float64{24, 16, 8, 0} {
			parts[i] = strconv.Itoa(int(math.Mod(math.Floor(n/math.Pow(2, s)), 256)))
		}
		return strings.Join(parts, ".")
	}
	// Leave a few addresses after the gateway for static use, as the UniFi UI does;
	// fall back to the hosts on the other side of the gateway for tiny subnets.
	var first, last float64
	switch {
	case ip+5 <= broadcast-1:
		first, last = ip+5, broadcast-1
	case ip+1 <= broadcast-1:
		first, last = ip+1, broadcast-1
	default:
		first, last = network+1, ip-1
	}
	return SubnetRange{Gateway: m[1], First: toIP(first), Last: toIP(last)}, nil
}

func summarizeWan(n *Obj) *Obj {
	g := n.Get
	var dns []any
	for _, k := range []string{"wan_dns1", "wan_dns2", "wan_dns3", "wan_dns4"} {
		if ojson.Truthy(g(k)) {
			dns = append(dns, g(k))
		}
	}
	var smartQueues any = U
	if ojson.Truthy(g("wan_smartq_enabled")) {
		smartQueues = unifi.Compact("download_kbps", g("wan_smartq_down_rate"), "upload_kbps", g("wan_smartq_up_rate"))
	}
	var static any = U
	if ojson.StrictEq(g("wan_type"), "static") {
		static = unifi.Compact("ip", g("wan_ip"), "netmask", g("wan_netmask"), "gateway", g("wan_gateway"))
	}
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"enabled", g("enabled"),
		"interface", g("wan_networkgroup"),
		"type", g("wan_type"),
		"vlan", ojson.If(ojson.Truthy(g("wan_vlan_enabled")), g("wan_vlan"), U),
		"static", static,
		"pppoe_username", ojson.If(ojson.StrictEq(g("wan_type"), "pppoe"), g("wan_username"), U),
		"dns", ojson.If(len(dns) > 0, dns, U),
		"load_balance_type", g("wan_load_balance_type"),
		"load_balance_weight", g("wan_load_balance_weight"),
		"failover_priority", g("wan_failover_priority"),
		"smart_queues", smartQueues,
		"isp_download_kbps", ojson.Get(g("wan_provider_capabilities"), "download_kilobits_per_second"),
		"isp_upload_kbps", ojson.Get(g("wan_provider_capabilities"), "upload_kilobits_per_second"),
	)
}

func summarizeDNSRecord(r *Obj) *Obj {
	g := r.Get
	return unifi.Compact(
		"id", g("_id"),
		"type", g("record_type"),
		"name", g("key"),
		"value", g("value"),
		"ttl", g("ttl"),
		"enabled", g("enabled"),
		"port", ojson.OrUndefined(g("port")),
		"priority", ojson.OrUndefined(g("priority")),
		"weight", ojson.OrUndefined(g("weight")),
	)
}

func summarizeUsergroup(g *Obj) *Obj {
	rate := func(v any) any {
		if f, ok := ojson.Num(v); ok && f >= 0 {
			return v
		}
		return "unlimited"
	}
	return unifi.Compact(
		"id", g.Get("_id"),
		"name", g.Get("name"),
		"download_kbps", rate(g.Get("qos_rate_max_down")),
		"upload_kbps", rate(g.Get("qos_rate_max_up")),
		"default", ojson.OrUndefined(g.Get("attr_no_delete")),
	)
}

func registerNetworks(c *Context) {
	client := c.Client

	// ---------------------------------------------------------------- LANs / VLANs

	netList := listing{Search: []string{"name", "purpose", "subnet", "vlan", "domain_name"}}
	c.add(Def{
		Toolset: "networks", Name: "unifi_list_networks", Title: "List networks",
		Description: "List configured networks (LANs/VLANs, WANs, VPNs) with subnet, VLAN ID and DHCP settings.",
		Input:       props(siteArg(), netList.args(), rawArg(), c.secretsArg(secretsPassphrases)),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "rest/networkconf", nil)
			if err != nil {
				return nil, err
			}
			return netList.result(a, arr(res), func(n *Obj) any { return unifi.SummarizeNetwork(n) }, rawWithSecrets(c, a)), nil
		},
	})

	c.add(Def{
		Toolset: "networks", Name: "unifi_save_network", Title: "Create/update LAN or VLAN",
		Description: "Create a LAN/VLAN network (no id) or update one (id; only the fields passed change). Handles purposes corporate (regular LAN), guest and vlan-only. " +
			"On create, DHCP is enabled with a range derived from the subnet unless given. Use unifi_update_wan for WANs and unifi_save_vpn for VPNs. " +
			"Changing subnet/VLAN/DHCP disrupts connected clients.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("id", idSchema("Network").Opt().D("Network ID to update; omit to create")),
			P("name", Str().MinLen(1).Opt().D("Network name (required on create)")),
			P("purpose", Enum("corporate", "guest", "vlan-only").Opt().D("corporate (default), guest, or vlan-only (VLAN with no gateway/DHCP)")),
			P("vlan", Int().Min(2).Max(4095).Null().Opt().D("VLAN ID; null removes VLAN tagging")),
			P("subnet", Str().Opt().D("Gateway IP and prefix, e.g. 192.168.10.1/24")),
			P("dhcp_enabled", Bool().Opt().D("Run the DHCP server on this network")),
			P("dhcp_start", Str().Opt().D("First address of the DHCP range")),
			P("dhcp_stop", Str().Opt().D("Last address of the DHCP range")),
			P("dhcp_lease_seconds", Int().Positive().Opt().D("DHCP lease time in seconds (default 86400)")),
			P("dhcp_dns", Array(Str()).Max(4).Opt().D("DNS servers handed out by DHCP (up to 4); empty array reverts to the gateway")),
			P("dhcp_gateway", Str().Null().Opt().D("Override the gateway handed out by DHCP; null reverts to automatic")),
			P("domain_name", Str().Opt().D("Domain name handed out by DHCP")),
			P("igmp_snooping", Bool().Opt()),
			P("isolated", Bool().Opt().D("Network isolation: block traffic to other local networks")),
			P("internet_access", Bool().Opt().D("Allow internet access (false blocks it)")),
			P("mdns", Bool().Opt().D("Enable multicast DNS forwarding for this network")),
			P("enabled", Bool().Opt()),
			P("extra", extraSchema().D("Additional raw networkconf fields, e.g. DHCP options (dhcpd_ntp_1, dhcpd_boot_enabled/dhcpd_boot_server/dhcpd_boot_filename, dhcpd_tftp_server, dhcpd_wins_1) or IPv6 (ipv6_interface_type, ipv6_ra_enabled). Merged last.")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			var existing *Obj
			if id != "" {
				var err error
				if existing, err = restGet(ctx, client, s, "networkconf", id); err != nil {
					return nil, err
				}
				if !ojson.Includes(lanPurposes, existing.Get("purpose")) {
					tool := "unifi_save_vpn"
					if ojson.StrictEq(existing.Get("purpose"), "wan") {
						tool = "unifi_update_wan"
					}
					return nil, fmt.Errorf(`Network %s has purpose "%s"; use %s instead`, id, ojson.String(existing.Get("purpose")), tool)
				}
			} else if !a.Truthy("name") {
				return nil, errorf("name is required when creating a network")
			}

			purpose := ojson.Coalesce(a.Get("purpose"), existing.Get("purpose"), "corporate")
			body := unifi.Defined(
				"name", a.Get("name"),
				"purpose", a.Get("purpose"),
				"domain_name", a.Get("domain_name"),
				"igmp_snooping", a.Get("igmp_snooping"),
				"network_isolation_enabled", a.Get("isolated"),
				"internet_access_enabled", a.Get("internet_access"),
				"mdns_enabled", a.Get("mdns"),
				"enabled", a.Get("enabled"),
				"dhcpd_enabled", a.Get("dhcp_enabled"),
				"dhcpd_start", a.Get("dhcp_start"),
				"dhcpd_stop", a.Get("dhcp_stop"),
				"dhcpd_leasetime", a.Get("dhcp_lease_seconds"),
				"ip_subnet", a.Get("subnet"),
			)

			if a.Has("vlan") {
				if a.Get("vlan") == nil {
					body.Set("vlan_enabled", false)
				} else {
					body.Set("vlan_enabled", true).Set("vlan", a.Get("vlan"))
				}
			}
			if dns := a.Arr("dhcp_dns"); dns != nil {
				body.Set("dhcpd_dns_enabled", len(dns) > 0)
				for i := 0; i < 4; i++ {
					var v any = ""
					if i < len(dns) && !ojson.IsNullish(dns[i]) {
						v = dns[i]
					}
					body.Set(fmt.Sprintf("dhcpd_dns_%d", i+1), v)
				}
			}
			if a.Has("dhcp_gateway") {
				if a.Get("dhcp_gateway") == nil {
					body.Set("dhcpd_gateway_enabled", false)
				} else {
					body.Set("dhcpd_gateway_enabled", true).Set("dhcpd_gateway", a.Get("dhcp_gateway"))
				}
			}

			if ojson.StrictEq(purpose, "vlan-only") {
				if existing == nil && ojson.IsNullish(a.Get("vlan")) {
					return nil, errorf("vlan is required for a vlan-only network")
				}
				if a.Truthy("subnet") || a.Truthy("dhcp_enabled") {
					return nil, errorf("vlan-only networks have no subnet or DHCP server")
				}
			}

			// Derive a DHCP range when the subnet changes and no range was given.
			if a.Truthy("subnet") && !ojson.StrictEq(purpose, "vlan-only") {
				r, err := subnetRange(a.Str("subnet"))
				if err != nil {
					return nil, err
				}
				if !a.Truthy("dhcp_start") && !a.Truthy("dhcp_stop") {
					body.Set("dhcpd_start", r.First).Set("dhcpd_stop", r.Last)
				}
			}

			if existing == nil {
				if !ojson.StrictEq(purpose, "vlan-only") && !a.Truthy("subnet") {
					return nil, errorf("subnet is required when creating a corporate or guest network")
				}
				defaults := ojson.New("purpose", purpose, "networkgroup", "LAN", "enabled", ojson.Coalesce(a.Get("enabled"), true))
				if !ojson.StrictEq(purpose, "vlan-only") {
					defaults.Set("dhcpd_enabled", ojson.Coalesce(a.Get("dhcp_enabled"), true)).
						Set("dhcpd_leasetime", ojson.Coalesce(a.Get("dhcp_lease_seconds"), 86400.0)).
						Set("dhcpd_dns_enabled", ojson.Coalesce(body.Get("dhcpd_dns_enabled"), false)).
						Set("dhcpd_gateway_enabled", ojson.Coalesce(body.Get("dhcpd_gateway_enabled"), false)).
						Set("igmp_snooping", ojson.Coalesce(a.Get("igmp_snooping"), false))
				}
				defaults.Set("vlan_enabled", ojson.Coalesce(body.Get("vlan_enabled"), false))
				body.Assign(ojson.Spread(defaults, body))
			}
			body.Assign(a.Get("extra"))

			saved, err := restSave(ctx, client, s, "networkconf", id, body)
			if err != nil {
				return nil, err
			}
			return unifi.SummarizeNetwork(saved), nil
		},
	})

	c.add(Def{
		Toolset: "networks", Name: "unifi_delete_network", Title: "Delete LAN or VLAN",
		Description: "Delete a LAN/VLAN network (purpose corporate, guest or vlan-only). WAN networks cannot be deleted here; VPNs use unifi_delete_vpn. Clients on the network lose connectivity.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Network")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			net, err := restGet(ctx, client, s, "networkconf", a.Str("id"))
			if err != nil {
				return nil, err
			}
			if ojson.Truthy(net.Get("attr_no_delete")) {
				return nil, fmt.Errorf(`Network "%s" is a built-in network and cannot be deleted`, ojson.String(net.Get("name")))
			}
			if !ojson.Includes(lanPurposes, net.Get("purpose")) {
				return nil, fmt.Errorf(`Network "%s" has purpose "%s"; only LAN/VLAN networks can be deleted with this tool`, ojson.String(net.Get("name")), ojson.String(net.Get("purpose")))
			}
			if err := restDelete(ctx, client, s, "networkconf", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id"), "name", net.Get("name")), nil
		},
	})

	// ---------------------------------------------------------------- WAN

	c.add(Def{
		Toolset: "networks", Name: "unifi_list_wans", Title: "List WAN connections",
		Description: "List WAN interfaces with connection type, VLAN, static/PPPoE settings, failover/load-balancing and Smart Queues, plus live WAN health.",
		Input:       props(siteArg(), listing{}.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			nets, err := restList(ctx, client, s, "networkconf")
			if err != nil {
				return nil, err
			}
			healthRes, err := client.SiteGet(ctx, s, "stat/health", nil)
			health := arr(healthRes)
			if err != nil {
				health = []any{}
			}
			wan := ojson.Find(health, func(h any) bool { return ojson.StrictEq(ojson.Get(h, "subsystem"), "wan") })
			var healthOut any = U
			if ojson.Truthy(wan) {
				healthOut = unifi.Compact(
					"status", ojson.Get(wan, "status"),
					"wan_ip", ojson.Get(wan, "wan_ip"),
					"isp", ojson.Coalesce(ojson.Get(wan, "isp_name"), ojson.Get(wan, "isp_organization")),
					"gateways", ojson.Get(wan, "num_gw"),
					"uptime_stats", ojson.Get(wan, "uptime_stats"),
				)
			}
			wans := ojson.Filter(nets, func(n any) bool { return ojson.StrictEq(ojson.Get(n, "purpose"), "wan") })
			return listing{}.result(a, wans, func(n *Obj) any { return summarizeWan(n) }, nil).Set("health", healthOut), nil
		},
	})

	c.add(Def{
		Toolset: "networks", Name: "unifi_update_wan", Title: "Update WAN settings",
		Description: "Change a WAN interface's connection type (DHCP/static/PPPoE), VLAN, DNS, failover/load balancing or Smart Queues. Only the fields passed change. " +
			"A wrong setting can take the site offline.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			idArg("WAN network (see unifi_list_wans)"),
			P("type", Enum("dhcp", "static", "pppoe").Opt().D("Connection type")),
			P("static_ip", Str().Opt().D("Static WAN IP (type static)")),
			P("netmask", Str().Opt().D("Static WAN netmask, e.g. 255.255.255.0")),
			P("gateway", Str().Opt().D("Static WAN gateway")),
			P("dns", Array(Str()).Max(4).Opt().D("Manual DNS servers; empty array reverts to ISP-provided DNS")),
			P("pppoe_username", Str().Opt()),
			P("pppoe_password", Str().Opt()),
			P("vlan", Int().Min(1).Max(4095).Null().Opt().D("WAN VLAN ID (some ISPs require one); null disables")),
			P("load_balance_type", Enum("failover-only", "weighted").Opt().D("failover-only uses this WAN only when higher-priority WANs fail; weighted load-balances")),
			P("load_balance_weight", Int().Min(1).Max(99).Opt().D("Weight for weighted load balancing")),
			P("failover_priority", Int().Min(1).Opt().D("Failover priority (lower is preferred)")),
			P("smart_queues", Bool().Opt().D("Enable Smart Queues (SQM) to reduce bufferbloat")),
			P("download_kbps", Int().Positive().Opt().D("Smart Queues / ISP download rate in kbps")),
			P("upload_kbps", Int().Positive().Opt().D("Smart Queues / ISP upload rate in kbps")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			wan, err := restGet(ctx, client, s, "networkconf", id)
			if err != nil {
				return nil, err
			}
			if !ojson.StrictEq(wan.Get("purpose"), "wan") {
				return nil, fmt.Errorf(`Network %s ("%s") is not a WAN`, id, ojson.String(wan.Get("name")))
			}
			typ := ojson.Coalesce(a.Get("type"), wan.Get("wan_type"))
			if a.Str("type") == "static" && !ojson.Truthy(ojson.Coalesce(a.Get("static_ip"), wan.Get("wan_ip"))) {
				return nil, errorf("static_ip is required for a static WAN")
			}
			sqOn := !ojson.StrictEq(a.Get("smart_queues"), false)
			body := unifi.Defined(
				"wan_type", a.Get("type"),
				"wan_ip", a.Get("static_ip"),
				"wan_netmask", a.Get("netmask"),
				"wan_gateway", a.Get("gateway"),
				"wan_username", a.Get("pppoe_username"),
				"x_wan_password", a.Get("pppoe_password"),
				"wan_load_balance_type", a.Get("load_balance_type"),
				"wan_load_balance_weight", a.Get("load_balance_weight"),
				"wan_failover_priority", a.Get("failover_priority"),
				"wan_smartq_enabled", a.Get("smart_queues"),
				"wan_smartq_down_rate", ojson.If(sqOn, a.Get("download_kbps"), U),
				"wan_smartq_up_rate", ojson.If(sqOn, a.Get("upload_kbps"), U),
			)
			if (a.Truthy("static_ip") || a.Truthy("netmask") || a.Truthy("gateway")) && !ojson.StrictEq(typ, "static") {
				return nil, errorf("static_ip/netmask/gateway only apply to a static WAN (pass type: static)")
			}
			if (a.Truthy("pppoe_username") || a.Truthy("pppoe_password")) && !ojson.StrictEq(typ, "pppoe") {
				return nil, errorf("pppoe_username/pppoe_password only apply to a PPPoE WAN (pass type: pppoe)")
			}
			if a.Has("vlan") {
				if a.Get("vlan") == nil {
					body.Set("wan_vlan_enabled", false)
				} else {
					body.Set("wan_vlan_enabled", true).Set("wan_vlan", a.Get("vlan"))
				}
			}
			if dns := a.Arr("dns"); dns != nil {
				body.Set("wan_dns_preference", ojson.If(len(dns) > 0, "manual", "auto"))
				for i := 0; i < 4; i++ {
					var v any = ""
					if i < len(dns) && !ojson.IsNullish(dns[i]) {
						v = dns[i]
					}
					body.Set(fmt.Sprintf("wan_dns%d", i+1), v)
				}
			}
			if a.Truthy("download_kbps") || a.Truthy("upload_kbps") {
				body.Set("wan_provider_capabilities", ojson.Spread(
					wan.Get("wan_provider_capabilities"),
					unifi.Defined("download_kilobits_per_second", a.Get("download_kbps"), "upload_kilobits_per_second", a.Get("upload_kbps")),
				))
			}
			body.Assign(a.Get("extra"))
			if body.Len() == 0 {
				return nil, errorf("Nothing to update")
			}
			saved, err := restSave(ctx, client, s, "networkconf", id, body)
			if err != nil {
				return nil, err
			}
			return summarizeWan(saved), nil
		},
	})

	// ---------------------------------------------------------------- local DNS

	c.add(Def{
		Toolset: "networks", Name: "unifi_list_dns_records", Title: "List local DNS records",
		Description: "List local DNS records served by the gateway (v2 static-dns; UniFi Network 8.2+).",
		Input:       props(siteArg(), listing{}.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := v2List(ctx, client, c.site(a), "static-dns")
			if err != nil {
				return nil, err
			}
			return listing{}.result(a, list, func(r *Obj) any { return summarizeDNSRecord(r) }, nil), nil
		},
	})

	c.add(Def{
		Toolset: "networks", Name: "unifi_save_dns_record", Title: "Create/update local DNS record",
		Destructive: boolPtr(true),
		Description: "Create (no id) or update (id; only fields passed change) a local DNS record served by the gateway (UniFi Network 8.2+). " +
			"For A/AAAA the value is an IP; for CNAME a hostname; for MX/SRV set priority (and weight/port for SRV).",
		Gate: Write,
		Input: props(
			siteArg(),
			P("id", idSchema("DNS record").Opt().D("Record ID to update; omit to create")),
			P("type", Enum("A", "AAAA", "CNAME", "MX", "TXT", "SRV", "NS").Opt().D("Record type (default A on create)")),
			P("name", Str().Opt().D("Record name (domain), e.g. nas.home.arpa (required on create)")),
			P("value", Str().Opt().D("Record value: IP address, target hostname or text (required on create)")),
			P("ttl", Int().Min(0).Opt().D("TTL in seconds; 0 = automatic")),
			P("priority", Int().Min(0).Opt().D("MX/SRV priority")),
			P("weight", Int().Min(0).Opt().D("SRV weight")),
			P("port", Int().Min(0).Max(65535).Opt().D("SRV port")),
			P("enabled", Bool().Opt()),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			if id == "" && (!a.Truthy("name") || !a.Truthy("value")) {
				return nil, errorf("name and value are required when creating a DNS record")
			}
			body := unifi.Defined(
				"record_type", a.Get("type"),
				"key", a.Get("name"),
				"value", a.Get("value"),
				"ttl", a.Get("ttl"),
				"priority", a.Get("priority"),
				"weight", a.Get("weight"),
				"port", a.Get("port"),
				"enabled", a.Get("enabled"),
			)
			if id == "" {
				body.Set("record_type", ojson.Coalesce(a.Get("type"), "A")).
					Set("ttl", ojson.Coalesce(a.Get("ttl"), 0.0)).
					Set("enabled", ojson.Coalesce(a.Get("enabled"), true))
			}
			body.Assign(a.Get("extra"))
			saved, err := v2Save(ctx, client, c.site(a), "static-dns", id, body)
			if err != nil {
				return nil, err
			}
			return summarizeDNSRecord(asObj(saved)), nil
		},
	})

	c.add(Def{
		Toolset: "networks", Name: "unifi_delete_dns_record", Title: "Delete local DNS record",
		Description: "Delete a local DNS record.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("DNS record")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			if err := v2Delete(ctx, client, c.site(a), "static-dns", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id")), nil
		},
	})

	// ---------------------------------------------------------------- bandwidth profiles (user groups)

	c.add(Def{
		Toolset: "networks", Name: "unifi_list_bandwidth_profiles", Title: "List bandwidth profiles",
		Description: "List bandwidth profiles (user groups) that cap client download/upload rates; assign them to WLANs or clients.",
		Input:       props(siteArg(), listing{}.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := restList(ctx, client, c.site(a), "usergroup")
			if err != nil {
				return nil, err
			}
			return listing{}.result(a, list, func(g *Obj) any { return summarizeUsergroup(g) }, nil), nil
		},
	})

	c.add(Def{
		Toolset: "networks", Name: "unifi_save_bandwidth_profile", Title: "Create/update bandwidth profile",
		Destructive: boolPtr(true),
		Description: "Create (no id) or update (id) a bandwidth profile (user group). Rates are in kbps; -1 means unlimited.",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("id", idSchema("Bandwidth profile").Opt().D("Profile ID to update; omit to create")),
			P("name", Str().MinLen(1).Opt().D("Profile name (required on create)")),
			P("download_kbps", Int().Min(-1).Opt().D("Download cap in kbps (-1 = unlimited)")),
			P("upload_kbps", Int().Min(-1).Opt().D("Upload cap in kbps (-1 = unlimited)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			if id == "" && !a.Truthy("name") {
				return nil, errorf("name is required when creating a bandwidth profile")
			}
			body := unifi.Defined("name", a.Get("name"), "qos_rate_max_down", a.Get("download_kbps"), "qos_rate_max_up", a.Get("upload_kbps"))
			if id == "" {
				body.Set("qos_rate_max_down", ojson.Coalesce(a.Get("download_kbps"), -1.0)).Set("qos_rate_max_up", ojson.Coalesce(a.Get("upload_kbps"), -1.0))
			}
			saved, err := restSave(ctx, client, c.site(a), "usergroup", id, body)
			if err != nil {
				return nil, err
			}
			return summarizeUsergroup(saved), nil
		},
	})

	c.add(Def{
		Toolset: "networks", Name: "unifi_delete_bandwidth_profile", Title: "Delete bandwidth profile",
		Description: "Delete a bandwidth profile (user group). The default profile cannot be deleted.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Bandwidth profile")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			group, err := restGet(ctx, client, s, "usergroup", a.Str("id"))
			if err != nil {
				return nil, err
			}
			if ojson.Truthy(group.Get("attr_no_delete")) {
				return nil, fmt.Errorf(`"%s" is the default bandwidth profile and cannot be deleted`, ojson.String(group.Get("name")))
			}
			if err := restDelete(ctx, client, s, "usergroup", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id"), "name", group.Get("name")), nil
		},
	})

	// ---------------------------------------------------------------- dynamic DNS

	ddnsList := listing{Search: []string{"service", "hostname", "server", "interface"}}
	c.add(Def{
		Toolset: "networks", Name: "unifi_list_dynamic_dns", Title: "List dynamic DNS",
		Description: "List dynamic DNS configurations (passwords redacted unless include_secrets).",
		Input:       props(siteArg(), ddnsList.args(), c.secretsArg(secretsPassphrases)),
		Handler: func(ctx context.Context, a Args) (any, error) {
			entries, err := restList(ctx, client, c.site(a), "dynamicdns")
			if err != nil {
				return nil, err
			}
			shown := entries
			if !c.includeSecrets(a) {
				shown = arr(unifi.RedactSecrets(entries))
			}
			return ddnsList.result(a, shown, func(d *Obj) any {
				return unifi.Compact(
					"id", d.Get("_id"),
					"service", d.Get("service"),
					"hostname", d.Get("host_name"),
					"login", d.Get("login"),
					"password", d.Get("x_password"),
					"server", d.Get("server"),
					"interface", d.Get("interface"),
					"options", ojson.If(ojson.HasLength(d.Get("options")), d.Get("options"), U),
				)
			}, nil), nil
		},
	})

	c.add(Def{
		Toolset: "networks", Name: "unifi_save_dynamic_dns", Title: "Create/update dynamic DNS",
		Destructive: boolPtr(true),
		Description: "Create (no id) or update (id) a dynamic DNS configuration on the gateway.",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("id", idSchema("Dynamic DNS entry").Opt().D("Entry ID to update; omit to create")),
			P("service", Str().Opt().D("Provider, e.g. dyndns, noip, afraid, duckdns, dnspark, easydns, namecheap, zoneedit, cloudflare or custom (required on create)")),
			P("hostname", Str().Opt().D("Hostname to update (required on create)")),
			P("login", Str().Opt().D("Username / login")),
			P("password", Str().Opt().D("Password or API token")),
			P("server", Str().Opt().D("Update server (custom service)")),
			P("interface", Str().Opt().D("WAN interface: wan (default) or wan2")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			if id == "" && (!a.Truthy("service") || !a.Truthy("hostname")) {
				return nil, errorf("service and hostname are required when creating a dynamic DNS entry")
			}
			body := unifi.Defined(
				"service", a.Get("service"),
				"host_name", a.Get("hostname"),
				"login", a.Get("login"),
				"x_password", a.Get("password"),
				"server", a.Get("server"),
				"interface", a.Get("interface"),
			)
			if id == "" && ojson.IsNullish(body.Get("interface")) {
				body.Set("interface", "wan")
			}
			body.Assign(a.Get("extra"))
			saved, err := restSave(ctx, client, c.site(a), "dynamicdns", id, body)
			if err != nil {
				return nil, err
			}
			return unifi.Compact(
				"id", ojson.Coalesce(saved.Get("_id"), a.Get("id")),
				"service", saved.Get("service"),
				"hostname", saved.Get("host_name"),
				"interface", saved.Get("interface"),
			), nil
		},
	})
}
