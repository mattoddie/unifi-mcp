package tools

// unifi_check_firewall_path: a read-only "what if" for the zone-based
// firewall. It works out the source and destination zones of a connection
// from the networks' subnets, then walks the zone policies between them in
// order and reports the first one that matches. Criteria it can't evaluate
// offline (apps, domains, regions, schedules, object groups) make a policy
// "maybe", and those are reported instead of being guessed.

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// tri is a three-valued match result.
type tri int

const (
	no tri = iota
	maybe
	yes
)

func (t tri) String() string { return [...]string{"no", "maybe", "yes"}[t] }

// verdict collects the criteria of one policy or rule: the result is no if
// any criterion is no, else maybe if any is maybe (with the reasons), else yes.
type verdict struct {
	result  tri
	reasons []string
}

func newVerdict() *verdict { return &verdict{result: yes} }

func (v *verdict) add(t tri, reason string) {
	switch {
	case t == no:
		v.result = no
	case t == maybe && v.result == yes:
		v.result = maybe
	}
	if t == maybe && reason != "" {
		v.reasons = append(v.reasons, reason)
	}
}

// pathHost is one end of the connection being checked.
type pathHost struct {
	ip      netip.Addr
	mac     string
	network *Obj // nil when outside every local subnet
	zone    *Obj
}

func (h *pathHost) summary() *Obj {
	out := ojson.New("ip", h.ip.String())
	if h.mac != "" {
		out.Set("mac", h.mac)
	}
	if h.network != nil {
		out.Set("network", unifi.Compact("id", h.network.Get("_id"), "name", h.network.Get("name"), "subnet", h.network.Get("ip_subnet")))
	}
	if h.zone != nil {
		out.Set("zone", unifi.Compact("id", h.zone.Get("_id"), "name", h.zone.Get("name"), "zone_key", h.zone.Get("zone_key")))
	}
	return out
}

func (h *pathHost) networkID() any {
	if h.network == nil {
		return U
	}
	return h.network.Get("_id")
}

// ipSpecMatches matches an address against "1.2.3.4", "10.0.0.0/8" or
// "10.0.0.1-10.0.0.9".
func ipSpecMatches(ip netip.Addr, spec string) tri {
	spec = strings.TrimSpace(spec)
	if from, to, ok := strings.Cut(spec, "-"); ok {
		a, err1 := netip.ParseAddr(strings.TrimSpace(from))
		b, err2 := netip.ParseAddr(strings.TrimSpace(to))
		if err1 != nil || err2 != nil {
			return maybe
		}
		if ip.Compare(a) >= 0 && ip.Compare(b) <= 0 {
			return yes
		}
		return no
	}
	if strings.Contains(spec, "/") {
		p, err := netip.ParsePrefix(spec)
		if err != nil {
			return maybe
		}
		if p.Masked().Contains(ip) {
			return yes
		}
		return no
	}
	a, err := netip.ParseAddr(spec)
	if err != nil {
		return maybe
	}
	if a == ip {
		return yes
	}
	return no
}

// portSpecMatches matches a port against "53", "80,443" or "8000-8100".
// port 0 means the port isn't known.
func portSpecMatches(port int, spec string) tri {
	if port == 0 {
		return maybe
	}
	result := no
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		lo, hi, isRange := strings.Cut(part, "-")
		if !isRange {
			hi = lo
		}
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil {
			result = maybe
			continue
		}
		if port >= a && port <= b {
			return yes
		}
	}
	return result
}

func negate(t tri, on bool) tri {
	if !on || t == maybe {
		return t
	}
	return yes - t
}

// matchEndpoint evaluates one side (source or destination) of a zone policy.
func matchEndpoint(v *verdict, side string, e *Obj, h *pathHost, port int) {
	if e == nil {
		return
	}
	g := e.Get
	target := sval(ojson.Coalesce(g("matching_target"), "ANY"))
	opposite := ojson.Truthy(g("match_opposite_ips"))
	switch target {
	case "ANY":
	case "IP":
		if ojson.StrictEq(g("matching_target_type"), "OBJECT") || ojson.Truthy(g("ip_group_id")) {
			v.add(maybe, side+" matches an IP group ("+sval(g("ip_group_id"))+"), which isn't evaluated")
			break
		}
		t := no
		for _, ip := range arr(g("ips")) {
			if r := ipSpecMatches(h.ip, sval(ip)); r > t {
				t = r
			}
		}
		v.add(negate(t, opposite), side+" IP list has entries that couldn't be parsed")
	case "NETWORK":
		t := no
		if h.network != nil && ojson.Includes(arr(g("network_ids")), h.networkID()) {
			t = yes
		}
		v.add(negate(t, opposite), "")
	case "CLIENT":
		switch {
		case h.mac != "":
			t := no
			for _, m := range arr(g("client_macs")) {
				if strings.EqualFold(sval(m), h.mac) {
					t = yes
				}
			}
			v.add(negate(t, opposite), "")
		default:
			v.add(maybe, side+" matches clients by MAC, and "+h.ip.String()+" isn't a connected client")
		}
	default:
		v.add(maybe, fmt.Sprintf("%s matches by %s, which can't be evaluated from addresses", side, strings.ToLower(target)))
	}

	pmt := sval(ojson.Coalesce(g("port_matching_type"), "ANY"))
	oppositePorts := ojson.Truthy(g("match_opposite_ports"))
	switch pmt {
	case "ANY":
	case "SPECIFIC":
		if side == "source" {
			v.add(maybe, "source port "+sval(g("port"))+" (the client's source port isn't known)")
			break
		}
		t := portSpecMatches(port, sval(g("port")))
		reason := "destination port " + sval(g("port"))
		if port == 0 {
			reason += " (no port given)"
		}
		v.add(negate(t, oppositePorts), reason)
	default:
		v.add(maybe, side+" matches a port group ("+sval(g("port_group_id"))+"), which isn't evaluated")
	}
}

// matchProtocol compares a policy's protocol with the connection's.
func matchProtocol(policy, proto string, v6 bool) tri {
	switch strings.ToLower(policy) {
	case "", "all":
		return yes
	case "tcp_udp":
		if proto == "tcp" || proto == "udp" {
			return yes
		}
		return no
	case "icmp":
		if proto == "icmp" && !v6 {
			return yes
		}
		return no
	case "icmpv6":
		if proto == "icmp" && v6 {
			return yes
		}
		return no
	}
	if strings.EqualFold(policy, proto) {
		return yes
	}
	return no
}

// matchPolicy evaluates a zone policy against a new connection.
func matchPolicy(p *Obj, src, dst *pathHost, proto string, port int) *verdict {
	v := newVerdict()
	v6 := src.ip.Is6()
	v.add(matchProtocol(sval(p.Get("protocol")), proto, v6), "")
	switch sval(p.Get("ip_version")) {
	case "IPV4":
		if v6 {
			v.add(no, "")
		}
	case "IPV6":
		if !v6 {
			v.add(no, "")
		}
	}
	// A new connection only matches policies that apply to new connections.
	switch sval(p.Get("connection_state_type")) {
	case "RESPOND_ONLY":
		v.add(no, "")
	case "CUSTOM":
		if !ojson.Includes(arr(p.Get("connection_states")), "NEW") {
			v.add(no, "")
		}
	}
	if proto == "icmp" {
		// Port criteria imply TCP or UDP.
		for _, e := range []any{p.Get("source"), p.Get("destination")} {
			if pmt := ojson.Get(e, "port_matching_type"); ojson.Truthy(pmt) && !ojson.StrictEq(pmt, "ANY") {
				v.add(no, "")
			}
		}
	}
	src0, _ := ojson.AsObj(p.Get("source"))
	dst0, _ := ojson.AsObj(p.Get("destination"))
	matchEndpoint(v, "source", src0, src, 0)
	matchEndpoint(v, "destination", dst0, dst, port)
	if s := nonDefaultSchedule(p.Get("schedule")); !ojson.IsUndefined(s) {
		v.add(maybe, "only applies on its schedule ("+sval(ojson.Get(s, "mode"))+")")
	}
	return v
}

// matchTrafficRule evaluates a traffic rule (v2 trafficrules) against the connection.
func matchTrafficRule(r *Obj, src, dst *pathHost, port int) *verdict {
	v := newVerdict()
	targets := arr(r.Get("target_devices"))
	t := no
	if len(targets) == 0 {
		t = maybe
	}
	for _, d := range targets {
		r := no
		switch sval(ojson.Get(d, "type")) {
		case "ALL_CLIENTS":
			r = yes
		case "CLIENT":
			if src.mac == "" {
				r = maybe
			} else if strings.EqualFold(sval(ojson.Get(d, "client_mac")), src.mac) {
				r = yes
			}
		case "NETWORK":
			if src.network != nil && ojson.StrictEq(ojson.Get(d, "network_id"), src.networkID()) {
				r = yes
			}
		default:
			r = maybe
		}
		if r > t {
			t = r
		}
	}
	v.add(t, "the rule's targets couldn't all be matched to the source")

	switch target := sval(r.Get("matching_target")); target {
	case "INTERNET":
		if dst.network != nil {
			v.add(no, "")
		}
	case "IP":
		m := no
		for _, e := range arr(r.Get("ip_addresses")) {
			spec := sval(ojson.Coalesce(ojson.Get(e, "ip_or_subnet"), e))
			ipMatch := ipSpecMatches(dst.ip, spec)
			if ipMatch != no && (ojson.HasLength(ojson.Get(e, "ports")) || ojson.HasLength(ojson.Get(e, "port_ranges"))) {
				ports := []string{}
				for _, p := range arr(ojson.Get(e, "ports")) {
					ports = append(ports, sval(p))
				}
				pm := maybe
				if len(ports) > 0 && !ojson.HasLength(ojson.Get(e, "port_ranges")) {
					pm = portSpecMatches(port, strings.Join(ports, ","))
				}
				ipMatch = min(ipMatch, pm)
			}
			m = max(m, ipMatch)
		}
		for _, e := range arr(r.Get("ip_ranges")) {
			m = max(m, ipSpecMatches(dst.ip, sval(ojson.Get(e, "ip_start"))+"-"+sval(ojson.Get(e, "ip_stop"))))
		}
		v.add(m, "the rule's IP entries have port ranges or couldn't be parsed")
	default:
		v.add(maybe, "matches by "+strings.ToLower(target)+", which can't be evaluated from addresses")
	}
	if s := nonDefaultSchedule(r.Get("schedule")); !ojson.IsUndefined(s) {
		v.add(maybe, "only applies on its schedule ("+sval(ojson.Get(s, "mode"))+")")
	}
	return v
}

// networkFor finds the most specific local network whose subnet holds ip.
func networkFor(networks []any, ip netip.Addr) *Obj {
	var best *Obj
	bits := -1
	for _, n := range networks {
		p, err := netip.ParsePrefix(sval(ojson.Get(n, "ip_subnet")))
		if err != nil || !p.Masked().Contains(ip) || p.Bits() <= bits {
			continue
		}
		best, bits = asObj(n), p.Bits()
	}
	return best
}

func registerFirewallWhatIf(c *Context) {
	client := c.Client
	c.add(Def{
		Toolset: "firewall", Name: "unifi_check_firewall_path", Title: "Check which firewall policy applies",
		Description: "Read-only \"what if\": for a new connection from source to destination (and destination port), work out the source and destination zones from the networks' subnets (addresses outside every local subnet are in the External zone), then walk the enabled zone-based firewall policies between those zones in index order and report the first that matches, so the verdict (ALLOW, BLOCK or REJECT). " +
			"Policies that might match first but can't be evaluated from addresses (apps, domains, regions, IP or port groups, schedules) are listed under may_match_first, and when one of them is listed the verdict is only certain if it doesn't match. Traffic rules that match the source and destination are listed too. " +
			"It reads the configuration only: no traffic is sent, and the controller's own evaluation can differ in details. Network 9.0+ (zone-based firewall).",
		Input: props(
			siteArg(),
			P("source", Str().MinLen(1).D("Source IP address, or the MAC address of a connected client")),
			P("destination", Str().MinLen(1).D("Destination IP address, or the MAC address of a connected client")),
			P("protocol", Enum("tcp", "udp", "icmp").Opt().D("Protocol (default tcp)")),
			P("port", Int().Min(1).Max(65535).Opt().D("Destination port (tcp/udp). Without it, policies that match specific ports are reported as may_match_first.")),
			P("source_zone_id", Str().Opt().D("Use this zone for the source instead of working it out (v2 _id from unifi_list_firewall_zones), e.g. the Gateway zone")),
			P("destination_zone_id", Str().Opt().D("Use this zone for the destination instead of working it out, e.g. the Gateway zone for the gateway's own services")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			networks, err := restList(ctx, client, s, "networkconf")
			if err != nil {
				return nil, err
			}
			zones, err := v2List(ctx, client, s, "firewall/zone")
			if err != nil {
				return nil, err
			}
			var clients []any
			clientsLoaded := false
			loadClients := func() ([]any, error) {
				if !clientsLoaded {
					res, err := client.SiteGet(ctx, s, "stat/sta", nil)
					if err != nil {
						return nil, err
					}
					clients, clientsLoaded = arr(res), true
				}
				return clients, nil
			}
			resolve := func(side, value, zoneOverride string) (*pathHost, error) {
				h := &pathHost{}
				if ip, err := netip.ParseAddr(value); err == nil {
					h.ip = ip.Unmap()
					list, err := loadClients()
					if err != nil {
						return nil, err
					}
					if cl := ojson.Find(list, func(x any) bool { return ojson.StrictEq(ojson.Get(x, "ip"), h.ip.String()) }); !ojson.IsUndefined(cl) {
						h.mac = strings.ToLower(sval(ojson.Get(cl, "mac")))
					}
				} else {
					mac, err := unifi.NormalizeMac(value)
					if err != nil {
						return nil, fmt.Errorf("%s must be an IP address or a client MAC address, got %q", side, value)
					}
					list, err := loadClients()
					if err != nil {
						return nil, err
					}
					cl := ojson.Find(list, func(x any) bool { return strings.EqualFold(sval(ojson.Get(x, "mac")), mac) })
					ip, err := netip.ParseAddr(sval(ojson.Get(cl, "ip")))
					if err != nil {
						return nil, fmt.Errorf("%s %s isn't a connected client with an IP address; pass its IP instead", side, mac)
					}
					h.ip, h.mac = ip.Unmap(), mac
				}
				h.network = networkFor(networks, h.ip)
				var zone any
				switch {
				case zoneOverride != "":
					zone = ojson.Find(zones, func(z any) bool { return ojson.StrictEq(ojson.Get(z, "_id"), zoneOverride) })
					if ojson.IsUndefined(zone) {
						return nil, fmt.Errorf("No firewall zone with id %q; see unifi_list_firewall_zones", zoneOverride)
					}
				case h.network == nil:
					zone = ojson.Find(zones, func(z any) bool { return ojson.StrictEq(ojson.Get(z, "zone_key"), "external") })
					if ojson.IsUndefined(zone) {
						return nil, fmt.Errorf("%s %s is outside every local subnet, and no zone has zone_key external; pass %s_zone_id", side, h.ip, side)
					}
				default:
					zone = ojson.Find(zones, func(z any) bool { return ojson.Includes(arr(ojson.Get(z, "network_ids")), h.networkID()) })
					if ojson.IsUndefined(zone) {
						return nil, fmt.Errorf("Network %q (%s) isn't in any firewall zone; pass %s_zone_id", sval(h.network.Get("name")), side, side)
					}
				}
				h.zone = asObj(zone)
				return h, nil
			}
			src, err := resolve("source", a.Str("source"), a.Str("source_zone_id"))
			if err != nil {
				return nil, err
			}
			dst, err := resolve("destination", a.Str("destination"), a.Str("destination_zone_id"))
			if err != nil {
				return nil, err
			}
			if src.ip.Is4() != dst.ip.Is4() {
				return nil, errorf("source and destination must both be IPv4 or both IPv6")
			}
			proto := a.Str("protocol")
			if proto == "" {
				proto = "tcp"
			}
			port := 0
			// The schema limits port to an integer from 1 to 65535.
			if n, err := strconv.Atoi(sval(a.Get("port"))); err == nil && proto != "icmp" && n >= 1 && n <= 65535 {
				port = n
			}

			policies, err := v2List(ctx, client, s, "firewall-policies")
			if err != nil {
				return nil, err
			}
			srcZone, dstZone := src.zone.Get("_id"), dst.zone.Get("_id")
			candidates := ojson.Filter(policies, func(p any) bool {
				return !ojson.StrictEq(ojson.Get(p, "enabled"), false) &&
					ojson.StrictEq(ojson.Get(p, "source", "zone_id"), srcZone) &&
					ojson.StrictEq(ojson.Get(p, "destination", "zone_id"), dstZone)
			})
			sort.SliceStable(candidates, func(i, j int) bool {
				return ojson.ToNumber(ojson.Get(candidates[i], "index")) < ojson.ToNumber(ojson.Get(candidates[j], "index"))
			})
			brief := func(p *Obj) *Obj {
				return unifi.Compact("id", p.Get("_id"), "name", p.Get("name"), "index", p.Get("index"), "action", p.Get("action"), "predefined", ojson.OrUndefined(p.Get("predefined")))
			}
			mayMatch := []any{}
			var decided any = U
			verdictAction := "unknown"
			for _, p := range candidates {
				po := asObj(p)
				v := matchPolicy(po, src, dst, proto, port)
				if v.result == maybe {
					mayMatch = append(mayMatch, brief(po).Set("unknown", ojson.Strings(v.reasons)))
				}
				if v.result == yes {
					decided, verdictAction = brief(po), sval(po.Get("action"))
					break
				}
			}

			rules, err := v2List(ctx, client, s, "trafficrules")
			if err != nil {
				rules = nil
			}
			ruleMatches, ruleMaybe := []any{}, []any{}
			for _, r := range rules {
				ro := asObj(r)
				if ojson.StrictEq(ro.Get("enabled"), false) {
					continue
				}
				v := matchTrafficRule(ro, src, dst, port)
				item := unifi.Compact("id", ro.Get("_id"), "description", ro.Get("description"), "action", ro.Get("action"), "matching_target", ro.Get("matching_target"))
				switch v.result {
				case yes:
					ruleMatches = append(ruleMatches, item)
				case maybe:
					ruleMaybe = append(ruleMaybe, item.Set("unknown", ojson.Strings(v.reasons)))
				}
			}

			out := ojson.New(
				"source", src.summary(),
				"destination", dst.summary(),
				"protocol", proto,
				"port", ojson.If(port > 0, float64(port), U),
				"verdict", verdictAction,
				"decided_by", decided,
				"certain", !ojson.IsUndefined(decided) && len(mayMatch) == 0,
				"may_match_first", mayMatch,
				"policies_checked", float64(len(candidates)),
				"traffic_rules", ojson.New("matching", ruleMatches, "may_match", ruleMaybe),
			)
			if ojson.IsUndefined(decided) {
				out.Set("note", "No enabled policy between these zones definitely matches. Check may_match_first, or the zones' default policies with unifi_list_firewall_policies.")
			}
			return unifi.CompactObj(out), nil
		},
	})
}

// sval is a value as a string, with "" for undefined and null (ojson.String
// gives "undefined", as JavaScript does).
func sval(v any) string {
	if ojson.IsNullish(v) {
		return ""
	}
	return ojson.String(v)
}
