package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

const secretsVPN = "Include secret fields such as pre-shared keys, private keys and passwords (redacted by default)"

var vpnPurposes = ojson.Strings([]string{"remote-user-vpn", "site-vpn", "vpn-client"})

// vpnTypes maps a friendly VPN kind to the networkconf purpose and vpn_type.
var vpnTypes = []struct{ kind, purpose, vpnType string }{
	{"wireguard-server", "remote-user-vpn", "wireguard-server"},
	{"openvpn-server", "remote-user-vpn", "openvpn-server"},
	{"l2tp-server", "remote-user-vpn", "l2tp-server"},
	{"wireguard-client", "vpn-client", "wireguard-client"},
	{"openvpn-client", "vpn-client", "openvpn-client"},
	{"ipsec-site-to-site", "site-vpn", "ipsec-vpn"},
	{"openvpn-site-to-site", "site-vpn", "openvpn-vpn"},
}

func vpnKinds() []string {
	out := make([]string, len(vpnTypes))
	for i, t := range vpnTypes {
		out[i] = t.kind
	}
	return out
}

func vpnType(kind string) (purpose, vt string) {
	for _, t := range vpnTypes {
		if t.kind == kind {
			return t.purpose, t.vpnType
		}
	}
	return "", ""
}

func vpnKind(n *Obj) string {
	for _, t := range vpnTypes {
		if ojson.StrictEq(n.Get("purpose"), t.purpose) && ojson.StrictEq(n.Get("vpn_type"), t.vpnType) {
			return t.kind
		}
	}
	return ojson.String(n.Get("purpose")) + "/" + ojson.String(ojson.Coalesce(n.Get("vpn_type"), "?"))
}

func summarizeVPN(n *Obj) *Obj {
	g := n.Get
	var dns []any
	for _, k := range []string{"dhcpd_dns_1", "dhcpd_dns_2"} {
		if ojson.Truthy(g(k)) {
			dns = append(dns, g(k))
		}
	}
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"kind", vpnKind(n),
		"enabled", g("enabled"),
		"subnet", g("ip_subnet"),
		"port", g("local_port"),
		"wan_interface", ojson.Coalesce(g("l2tp_interface"), g("wireguard_interface"), g("openvpn_interface"), g("ipsec_interface")),
		"peer_ip", ojson.Coalesce(g("ipsec_peer_ip"), g("openvpn_remote_host")),
		"local_ip", g("ipsec_local_ip"),
		"remote_subnets", ojson.If(ojson.HasLength(g("remote_vpn_subnets")), g("remote_vpn_subnets"), U),
		"key_exchange", g("ipsec_key_exchange"),
		"radius_profile_id", g("radiusprofile_id"),
		"dns", ojson.If(len(dns) > 0, dns, U),
	)
}

func summarizeRadiusProfile(p *Obj, includeSecrets bool) *Obj {
	servers := func(v any) any {
		if !ojson.HasLength(v) {
			return U
		}
		return mapObjs(v, func(s *Obj) any {
			return unifi.Compact("ip", s.Get("ip"), "port", s.Get("port"), "secret", ojson.If(includeSecrets, s.Get("x_secret"), U))
		})
	}
	g := p.Get
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"auth_servers", servers(g("auth_servers")),
		"accounting_enabled", g("accounting_enabled"),
		"acct_servers", servers(g("acct_servers")),
		"vlan_enabled", ojson.OrUndefined(g("vlan_enabled")),
		"vlan_wlan_mode", ojson.If(ojson.Truthy(g("vlan_enabled")), g("vlan_wlan_mode"), U),
		"built_in", ojson.OrUndefined(g("attr_no_delete")),
	)
}

func summarizeRadiusAccount(a *Obj, includeSecrets bool) *Obj {
	g := a.Get
	return unifi.Compact(
		"id", g("_id"),
		"name", g("name"),
		"password", ojson.If(includeSecrets, g("x_password"), U),
		"vlan", g("vlan"),
		"tunnel_type", g("tunnel_type"),
		"tunnel_medium_type", g("tunnel_medium_type"),
		"network_id", g("networkconf_id"),
	)
}

func serverArg() *Schema {
	return Object(
		P("ip", Str().D("RADIUS server IP")),
		P("port", Int().Min(1).Max(65535).Opt().D("Port (1812 auth / 1813 accounting by default)")),
		P("secret", Str().D("Shared secret")),
	)
}

func registerVPN(c *Context) {
	client := c.Client
	// Explicit search fields keep secrets (shown with include_secrets) out of search.
	vpnList := listing{Search: []string{"name", "kind", "subnet", "peer_ip", "remote_subnets"}}
	radiusProfileList := listing{Search: []string{"name", "auth_servers.ip"}}
	nameList := listing{Search: []string{"name"}}

	// ---------------------------------------------------------------- VPN networks

	c.add(Def{
		Toolset: "vpn", Name: "unifi_list_vpns", Title: "List VPNs",
		Description: "List VPN servers (WireGuard, OpenVPN, L2TP), VPN clients and site-to-site VPNs configured on the gateway. Use raw: true to see every field.",
		Input:       props(siteArg(), vpnList.args(), rawArg(), c.secretsArg(secretsVPN)),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := restList(ctx, client, c.site(a), "networkconf")
			if err != nil {
				return nil, err
			}
			vpns := ojson.Filter(list, func(n any) bool { return ojson.Includes(vpnPurposes, ojson.Get(n, "purpose")) })
			return vpnList.result(a, vpns, func(n *Obj) any { return summarizeVPN(n) }, rawWithSecrets(c, a)), nil
		},
	})

	c.add(Def{
		Toolset: "vpn", Name: "unifi_save_vpn", Title: "Create/update VPN",
		Description: "Create (no id; kind required) or update (id; only fields passed change) a VPN server, VPN client or site-to-site VPN. " +
			"Typed fields cover the common settings; anything else (IPsec proposals, OpenVPN/WireGuard client configuration, peer keys) goes in `extra` using the controller's field names — " +
			"inspect an existing VPN with unifi_list_vpns raw: true to see them, since they vary between Network versions. " +
			"WireGuard server peers (users) and imported client config files are not covered by this tool; use unifi_api_request.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("id", idSchema("VPN").Opt().D("VPN ID to update; omit to create")),
			P("kind", Enum(vpnKinds()...).Opt().D("VPN type (required on create)")),
			P("name", Str().MinLen(1).Opt().D("Name (required on create)")),
			P("enabled", Bool().Opt()),
			P("subnet", Str().Opt().D("Tunnel/client address pool as gateway/prefix, e.g. 192.168.3.1/24 (VPN servers)")),
			P("port", Int().Min(1).Max(65535).Opt().D("Listen port (WireGuard default 51820, OpenVPN 1194)")),
			P("wan_interface", Str().Opt().D("WAN to listen on: wan (default) or wan2 (L2TP/OpenVPN/WireGuard servers)")),
			P("pre_shared_key", Str().Opt().D("IPsec pre-shared key (L2TP server, IPsec site-to-site)")),
			P("peer_ip", Str().Opt().D("Remote peer public IP or hostname (site-to-site)")),
			P("local_ip", Str().Opt().D("Local WAN IP used for the tunnel (IPsec site-to-site)")),
			P("remote_subnets", Array(Str()).Opt().D("Remote networks reachable over the tunnel, CIDR (site-to-site)")),
			P("radius_profile_id", Str().Opt().D("RADIUS profile authenticating L2TP users (see unifi_list_radius_profiles)")),
			P("dns", Array(Str()).Max(2).Opt().D("DNS servers given to VPN clients; empty array = automatic")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			kindArg := a.Str("kind")
			var existing *Obj
			if id != "" {
				var err error
				if existing, err = restGet(ctx, client, s, "networkconf", id); err != nil {
					return nil, err
				}
				if !ojson.Includes(vpnPurposes, existing.Get("purpose")) {
					return nil, fmt.Errorf(`Network %s ("%s") is not a VPN`, id, ojson.String(existing.Get("name")))
				}
				if kindArg != "" {
					purpose, vt := vpnType(kindArg)
					if !ojson.StrictEq(existing.Get("purpose"), purpose) || !ojson.StrictEq(existing.Get("vpn_type"), vt) {
						return nil, errorf("A VPN's kind cannot be changed; delete it and create a new one")
					}
				}
			} else if kindArg == "" || !a.Truthy("name") {
				return nil, errorf("kind and name are required when creating a VPN")
			}
			kind := kindArg
			if kind == "" {
				kind = vpnKind(existing)
			}

			body := unifi.Defined(
				"name", a.Get("name"),
				"enabled", a.Get("enabled"),
				"ip_subnet", a.Get("subnet"),
				"local_port", a.Get("port"),
				"x_ipsec_pre_shared_key", a.Get("pre_shared_key"),
				"ipsec_peer_ip", a.Get("peer_ip"),
				"ipsec_local_ip", a.Get("local_ip"),
				"remote_vpn_subnets", a.Get("remote_subnets"),
				"radiusprofile_id", a.Get("radius_profile_id"),
			)
			if a.Truthy("wan_interface") {
				field := "ipsec_interface"
				switch {
				case strings.HasPrefix(kind, "l2tp"):
					field = "l2tp_interface"
				case strings.HasPrefix(kind, "wireguard"):
					field = "wireguard_interface"
				case strings.HasPrefix(kind, "openvpn"):
					field = "openvpn_interface"
				}
				body.Set(field, a.Get("wan_interface"))
			}
			if dns := a.Arr("dns"); dns != nil {
				body.Set("dhcpd_dns_enabled", len(dns) > 0)
				for i, k := range []string{"dhcpd_dns_1", "dhcpd_dns_2"} {
					var v any = ""
					if i < len(dns) && !ojson.IsNullish(dns[i]) {
						v = dns[i]
					}
					body.Set(k, v)
				}
			}

			if existing == nil {
				purpose, vt := vpnType(kindArg)
				if purpose == "remote-user-vpn" && !a.Truthy("subnet") {
					return nil, errorf("subnet is required when creating a VPN server")
				}
				if purpose == "site-vpn" && (!a.Truthy("peer_ip") || !ojson.HasLength(a.Get("remote_subnets"))) {
					return nil, errorf("peer_ip and remote_subnets are required when creating a site-to-site VPN")
				}
				if (kindArg == "l2tp-server" || kindArg == "ipsec-site-to-site") && !a.Truthy("pre_shared_key") {
					return nil, errorf("pre_shared_key is required for L2TP and IPsec VPNs")
				}
				if kindArg == "l2tp-server" && !a.Truthy("radius_profile_id") {
					profiles, err := restList(ctx, client, s, "radiusprofile")
					if err != nil {
						profiles = []any{}
					}
					def := ojson.Coalesce(ojson.Find(profiles, func(p any) bool { return ojson.Truthy(ojson.Get(p, "attr_no_delete")) }), firstOf(profiles))
					if ojson.Truthy(def) {
						body.Set("radiusprofile_id", ojson.Get(def, "_id"))
					}
				}
				defaults := ojson.New("purpose", purpose, "vpn_type", vt, "enabled", ojson.Coalesce(a.Get("enabled"), true))
				switch kindArg {
				case "wireguard-server":
					defaults.Set("local_port", ojson.Coalesce(a.Get("port"), 51820.0))
				case "openvpn-server":
					defaults.Set("local_port", ojson.Coalesce(a.Get("port"), 1194.0))
				case "ipsec-site-to-site":
					defaults.Set("ipsec_key_exchange", "ikev2")
				}
				body.Assign(ojson.Spread(defaults, body))
			}
			body.Assign(a.Get("extra"))

			saved, err := restSave(ctx, client, s, "networkconf", id, body)
			if err != nil {
				return nil, err
			}
			return summarizeVPN(saved), nil
		},
	})

	c.add(Def{
		Toolset: "vpn", Name: "unifi_delete_vpn", Title: "Delete VPN",
		Description: "Delete a VPN server, VPN client or site-to-site VPN. Connected VPN users/tunnels drop.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("VPN")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			id := a.Str("id")
			net, err := restGet(ctx, client, s, "networkconf", id)
			if err != nil {
				return nil, err
			}
			if !ojson.Includes(vpnPurposes, net.Get("purpose")) {
				return nil, fmt.Errorf(`Network %s ("%s") is not a VPN; use unifi_delete_network`, id, ojson.String(net.Get("name")))
			}
			if err := restDelete(ctx, client, s, "networkconf", id); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id"), "name", net.Get("name")), nil
		},
	})

	// ---------------------------------------------------------------- RADIUS profiles

	c.add(Def{
		Toolset: "vpn", Name: "unifi_list_radius_profiles", Title: "List RADIUS profiles",
		Description: "List RADIUS profiles used by WPA-Enterprise WiFi, 802.1X switch ports and L2TP VPN (secrets redacted unless include_secrets).",
		Input:       props(siteArg(), radiusProfileList.args(), c.secretsArg(secretsVPN)),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := restList(ctx, client, c.site(a), "radiusprofile")
			if err != nil {
				return nil, err
			}
			include := c.includeSecrets(a)
			return radiusProfileList.result(a, list, func(p *Obj) any { return summarizeRadiusProfile(p, include) }, nil), nil
		},
	})

	c.add(Def{
		Toolset: "vpn", Name: "unifi_save_radius_profile", Title: "Create/update RADIUS profile",
		Destructive: boolPtr(true),
		Description: "Create (no id) or update (id; only fields passed change) a RADIUS profile pointing at external RADIUS servers. Server lists replace the existing ones.",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("id", idSchema("RADIUS profile").Opt().D("Profile ID to update; omit to create")),
			P("name", Str().MinLen(1).Opt().D("Name (required on create)")),
			P("auth_servers", Array(serverArg()).Opt().D("Authentication servers")),
			P("accounting_enabled", Bool().Opt()),
			P("acct_servers", Array(serverArg()).Opt().D("Accounting servers")),
			P("vlan_enabled", Bool().Opt().D("Assign VLANs from RADIUS attributes")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			if id == "" && (!a.Truthy("name") || !ojson.HasLength(a.Get("auth_servers"))) {
				return nil, errorf("name and auth_servers are required when creating a RADIUS profile")
			}
			servers := func(k string, port float64) any {
				list := a.Arr(k)
				if list == nil {
					return U
				}
				return mapObjs(list, func(s *Obj) any {
					return ojson.New("ip", s.Get("ip"), "port", ojson.Coalesce(s.Get("port"), port), "x_secret", s.Get("secret"))
				})
			}
			body := unifi.Defined(
				"name", a.Get("name"),
				"auth_servers", servers("auth_servers", 1812),
				"accounting_enabled", a.Get("accounting_enabled"),
				"acct_servers", servers("acct_servers", 1813),
				"vlan_enabled", a.Get("vlan_enabled"),
			)
			if id == "" {
				body.Set("accounting_enabled", ojson.Coalesce(a.Get("accounting_enabled"), false)).
					Set("acct_servers", ojson.Coalesce(body.Get("acct_servers"), []any{})).
					Set("vlan_enabled", ojson.Coalesce(a.Get("vlan_enabled"), false))
			}
			body.Assign(a.Get("extra"))
			saved, err := restSave(ctx, client, c.site(a), "radiusprofile", id, body)
			if err != nil {
				return nil, err
			}
			return summarizeRadiusProfile(saved, false), nil
		},
	})

	c.add(Def{
		Toolset: "vpn", Name: "unifi_delete_radius_profile", Title: "Delete RADIUS profile",
		Description: "Delete a RADIUS profile. The built-in default profile cannot be deleted.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("RADIUS profile")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			profile, err := restGet(ctx, client, s, "radiusprofile", a.Str("id"))
			if err != nil {
				return nil, err
			}
			if ojson.Truthy(profile.Get("attr_no_delete")) {
				return nil, fmt.Errorf(`"%s" is the built-in RADIUS profile and cannot be deleted`, ojson.String(profile.Get("name")))
			}
			if err := restDelete(ctx, client, s, "radiusprofile", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id"), "name", profile.Get("name")), nil
		},
	})

	// ---------------------------------------------------------------- RADIUS accounts

	c.add(Def{
		Toolset: "vpn", Name: "unifi_list_radius_accounts", Title: "List RADIUS accounts",
		Description: "List users of the built-in RADIUS server (used for L2TP VPN and WPA-Enterprise). Passwords redacted unless include_secrets.",
		Input:       props(siteArg(), nameList.args(), c.secretsArg(secretsVPN)),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := restList(ctx, client, c.site(a), "account")
			if err != nil {
				return nil, err
			}
			include := c.includeSecrets(a)
			return nameList.result(a, list, func(x *Obj) any { return summarizeRadiusAccount(x, include) }, nil), nil
		},
	})

	c.add(Def{
		Toolset: "vpn", Name: "unifi_save_radius_account", Title: "Create/update RADIUS account",
		Destructive: boolPtr(true),
		Description: "Create (no id) or update (id) a user of the built-in RADIUS server. For per-user VLAN assignment set vlan together with tunnel_type 13 (VLAN) and tunnel_medium_type 6 (802).",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("id", idSchema("RADIUS account").Opt().D("Account ID to update; omit to create")),
			P("name", Str().MinLen(1).Opt().D("Username (required on create)")),
			P("password", Str().MinLen(1).Opt().D("Password (required on create)")),
			P("vlan", Int().Min(1).Max(4095).Opt().D("VLAN ID to assign to this user")),
			P("tunnel_type", Int().Min(1).Max(13).Opt().D("RADIUS Tunnel-Type (3 = L2TP, 13 = VLAN)")),
			P("tunnel_medium_type", Int().Min(1).Max(15).Opt().D("RADIUS Tunnel-Medium-Type (1 = IPv4, 6 = 802)")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			if id == "" && (!a.Truthy("name") || !a.Truthy("password")) {
				return nil, errorf("name and password are required when creating a RADIUS account")
			}
			if a.Has("tunnel_type") != a.Has("tunnel_medium_type") {
				return nil, errorf("tunnel_type and tunnel_medium_type must be set together")
			}
			body := unifi.Defined(
				"name", a.Get("name"),
				"x_password", a.Get("password"),
				"vlan", a.Get("vlan"),
				"tunnel_type", a.Get("tunnel_type"),
				"tunnel_medium_type", a.Get("tunnel_medium_type"),
			).Assign(a.Get("extra"))
			saved, err := restSave(ctx, client, c.site(a), "account", id, body)
			if err != nil {
				return nil, err
			}
			return summarizeRadiusAccount(saved, false), nil
		},
	})

	c.add(Def{
		Toolset: "vpn", Name: "unifi_delete_radius_account", Title: "Delete RADIUS account",
		Description: "Delete a user of the built-in RADIUS server.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("RADIUS account")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			account, err := restGet(ctx, client, s, "account", a.Str("id"))
			if err != nil {
				return nil, err
			}
			if err := restDelete(ctx, client, s, "account", a.Str("id")); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("id"), "name", account.Get("name")), nil
		},
	})
}
