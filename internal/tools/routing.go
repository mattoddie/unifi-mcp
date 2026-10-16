package tools

import (
	"context"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

var routeTypes = []struct{ short, full string }{
	{"nexthop", "nexthop-route"},
	{"interface", "interface-route"},
	{"blackhole", "blackhole"},
}

func routeTypeFull(short string) any {
	for _, t := range routeTypes {
		if t.short == short {
			return t.full
		}
	}
	return U
}

// routeField reads a static route field: hyphenated keys ("static-route_network"); older objects may use underscores.
func routeField(r *Obj, name string) any {
	return ojson.Coalesce(r.Get("static-route_"+name), r.Get("static_route_"+name))
}

func summarizeRoute(r *Obj) *Obj {
	typ := routeField(r, "type")
	var short any = U
	for _, t := range routeTypes {
		if ojson.StrictEq(typ, t.full) {
			short = t.short
			break
		}
	}
	return unifi.Compact(
		"id", r.Get("_id"),
		"name", r.Get("name"),
		"enabled", r.Get("enabled"),
		"type", ojson.Coalesce(short, typ, r.Get("type")),
		"destination", routeField(r, "network"),
		"next_hop", routeField(r, "nexthop"),
		"interface", routeField(r, "interface"),
		"distance", routeField(r, "distance"),
	)
}

func summarizeTrafficRoute(r *Obj) *Obj {
	out := ojson.New(
		"id", r.Get("_id"),
		"description", r.Get("description"),
		"enabled", r.Get("enabled"),
		"matching_target", r.Get("matching_target"),
		"network_id", r.Get("network_id"),
		"next_hop", r.Get("next_hop"),
		"kill_switch", ojson.OrUndefined(r.Get("kill_switch_enabled")),
	)
	out.Assign(summarizeTrafficMatch(r))
	return unifi.CompactObj(out)
}

func registerRouting(c *Context) {
	client := c.Client

	// ---------------------------------------------------------------- static routes

	c.add(Def{
		Toolset: "routing", Name: "unifi_list_routes", Title: "List static routes",
		Description: "List user-defined static routes. With active=true, return the gateway's live routing table instead (stat/routing; not available on every version).",
		Input:       props(siteArg(), P("active", Bool().Opt().D("Return the active routing table (including system routes)"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			if a.Truthy("active") {
				return client.SiteGet(ctx, c.site(a), "stat/routing", nil)
			}
			list, err := restList(ctx, client, c.site(a), "routing")
			if err != nil {
				return nil, err
			}
			return mapObjs(list, func(r *Obj) any { return summarizeRoute(r) }), nil
		},
	})

	c.add(Def{
		Toolset: "routing", Name: "unifi_save_route", Title: "Create or update static route",
		Description: "Create a static route (name and destination required; next_hop for type=nexthop, interface for type=interface) or update one by id. Defaults on create: type nexthop, distance 1, enabled.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("id", Str().Opt().D("Route ID to update (only the fields passed are changed). Omit to create.")),
			P("name", Str().Opt()),
			P("enabled", Bool().Opt()),
			P("type", Enum("nexthop", "interface", "blackhole").Opt().D("nexthop: via a gateway IP; interface: out of an interface (WAN1/WAN2 or a network ID); blackhole: drop")),
			P("destination", Str().Opt().D("Destination network in CIDR form, e.g. 10.20.0.0/16")),
			P("next_hop", Str().Opt().D("Next-hop IP address (type=nexthop)")),
			P("interface", Str().Opt().D("Interface (type=interface), e.g. WAN1, WAN2 or a network ID")),
			P("distance", Int().Min(1).Max(255).Opt().D("Administrative distance")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			var typ any = U
			if t := a.Str("type"); t != "" {
				typ = routeTypeFull(t)
			}
			body := unifi.Defined(
				"name", a.Get("name"),
				"enabled", a.Get("enabled"),
				"static-route_type", typ,
				"static-route_network", a.Get("destination"),
				"static-route_nexthop", a.Get("next_hop"),
				"static-route_interface", a.Get("interface"),
				"static-route_distance", a.Get("distance"),
			)
			if id == "" {
				t := a.Str("type")
				if t == "" {
					t = "nexthop"
				}
				if !a.Truthy("name") || !a.Truthy("destination") {
					return nil, errorf("name and destination are required to create a route")
				}
				if t == "nexthop" && !a.Truthy("next_hop") {
					return nil, errorf("next_hop is required for a nexthop route")
				}
				if t == "interface" && !a.Truthy("interface") {
					return nil, errorf("interface is required for an interface route")
				}
				body = ojson.Spread(ojson.New(
					"type", "static-route",
					"enabled", true,
					"gateway_type", "default",
					"static-route_type", routeTypeFull(t),
					"static-route_distance", 1.0,
				), body)
			}
			saved, err := restSave(ctx, client, c.site(a), "routing", id, ojson.Spread(body, a.Get("extra")))
			if err != nil {
				return nil, err
			}
			return summarizeRoute(saved), nil
		},
	})

	c.add(Def{
		Toolset: "routing", Name: "unifi_delete_route", Title: "Delete static route",
		Description: "Delete a static route.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Route")),
		Handler:     deleteHandler(c, "routing", false),
	})

	// ---------------------------------------------------------------- traffic routes (policy-based routing, v2)

	c.add(Def{
		Toolset: "routing", Name: "unifi_list_traffic_routes", Title: "List traffic routes",
		Description: "List traffic routes (policy-based routing): which clients/networks and destinations are sent out a specific WAN or VPN client.",
		Input:       props(siteArg(), P("raw", Bool().Opt().D("Return the full unprocessed objects"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			routes, err := v2List(ctx, client, c.site(a), "trafficroutes")
			if err != nil {
				return nil, err
			}
			if a.Truthy("raw") {
				return routes, nil
			}
			return mapObjs(routes, func(r *Obj) any { return summarizeTrafficRoute(r) }), nil
		},
	})

	c.add(Def{
		Toolset: "routing", Name: "unifi_save_traffic_route", Title: "Create or update traffic route",
		Description: "Create a traffic route (description, matching_target and network_id required) or update one by id. Routes matching traffic from the target clients/networks via network_id (a WAN or VPN client network; see unifi_list_networks). " +
			"matching_target: DOMAIN (domains), IP (ip_addresses), REGION (regions) or INTERNET (all traffic; requires explicit client_macs/network_ids). Targets default to all clients for DOMAIN/IP/REGION.",
		Gate: Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("id", Str().Opt().D("Traffic route ID to update (only the fields passed are changed). Omit to create.")),
			P("description", Str().Opt().D("Route name")),
			P("enabled", Bool().Opt()),
			P("matching_target", Enum("DOMAIN", "IP", "REGION", "INTERNET").Opt()),
			P("network_id", Str().Opt().D("Network ID to route through (WAN or VPN client)")),
			targetArgs(),
			P("domains", Array(Str()).Opt().D("Domains (matching_target=DOMAIN)")),
			P("ip_addresses", Array(Str()).Opt().D("IPs or CIDRs (matching_target=IP)")),
			P("regions", Array(Str()).Opt().D("ISO country codes (matching_target=REGION)")),
			P("kill_switch", Bool().Opt().D("Block the traffic if the route's interface/VPN is down")),
			P("next_hop", Str().Opt().D("Optional next-hop IP")),
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
				"matching_target", a.Get("matching_target"),
				"network_id", a.Get("network_id"),
				"target_devices", targets,
				"domains", domainEntries(a.Arr("domains")),
				"ip_addresses", ipEntries(a.Arr("ip_addresses")),
				"regions", a.Get("regions"),
				"kill_switch_enabled", a.Get("kill_switch"),
				"next_hop", a.Get("next_hop"),
			)
			if id != "" {
				current, err := v2Get(ctx, client, s, "trafficroutes", id)
				if err != nil {
					return nil, err
				}
				merged := ojson.Spread(current, patch, a.Get("extra"))
				e, err := enc(id)
				if err != nil {
					return nil, err
				}
				saved, err := client.V2(ctx, "PUT", s, "trafficroutes/"+e, merged)
				if err != nil {
					return nil, err
				}
				return summarizeTrafficRoute(savedOr(saved, merged)), nil
			}
			if !a.Truthy("description") || !a.Truthy("matching_target") || !a.Truthy("network_id") {
				return nil, errorf("description, matching_target and network_id are required to create a traffic route")
			}
			if a.Str("matching_target") == "INTERNET" {
				explicit := false
				for _, t := range arr(patch.Get("target_devices")) {
					if !ojson.StrictEq(ojson.Get(t, "type"), "ALL_CLIENTS") {
						explicit = true
					}
				}
				if !explicit {
					return nil, errorf("INTERNET traffic routes need explicit client_macs or network_ids")
				}
			}
			body := ojson.Spread(ojson.New(
				"enabled", true,
				"domains", []any{},
				"ip_addresses", []any{},
				"ip_ranges", []any{},
				"regions", []any{},
				"kill_switch_enabled", false,
				"next_hop", "",
				"target_devices", []any{ojson.New("type", "ALL_CLIENTS")},
			), patch, a.Get("extra"))
			created, err := client.V2(ctx, "POST", s, "trafficroutes", body)
			if err != nil {
				return nil, err
			}
			var result any
			if list, ok := ojson.Arr(created); ok {
				result = firstOf(list)
			} else {
				result = ojson.Coalesce(created, body)
			}
			return summarizeTrafficRoute(asObj(result)), nil
		},
	})

	c.add(Def{
		Toolset: "routing", Name: "unifi_delete_traffic_route", Title: "Delete traffic route",
		Description: "Delete a traffic route (policy-based route).",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Traffic route")),
		Handler:     deleteHandler(c, "trafficroutes", true),
	})
}
