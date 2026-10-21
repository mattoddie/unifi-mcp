package tools

import (
	"context"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// trafficFlowArrays are the filter arrays the v2 traffic-flows query expects in every request body.
var trafficFlowArrays = []string{
	"risk", "action", "direction", "protocol", "service", "policy", "policy_type",
	"source_mac", "source_ip", "source_host", "source_port", "source_domain", "source_network_id", "source_zone_id", "source_region",
	"destination_mac", "destination_ip", "destination_host", "destination_port", "destination_domain", "destination_network_id", "destination_zone_id", "destination_region",
	"in_network_id", "out_network_id", "next_ai_query", "except_for",
}

func flowEndpoint(e any) any {
	return unifi.Compact(
		"name", ojson.Coalesce(ojson.Get(e, "client_name"), ojson.Get(e, "host_name")),
		"mac", ojson.Get(e, "mac"),
		"ip", ojson.Get(e, "ip"),
		"port", ojson.Get(e, "port"),
		"network", ojson.Get(e, "network_name"),
		"zone", ojson.Get(e, "zone_name"),
		"domains", ojson.If(ojson.HasLength(ojson.Get(e, "domains")), ojson.Get(e, "domains"), U),
		"region", ojson.Get(e, "region"),
	)
}

func summarizeFlow(f *Obj) *Obj {
	g := f.Get
	data := g("traffic_data")
	var policies any = U
	if list, ok := ojson.Arr(g("policies")); ok && len(list) > 0 {
		policies = ojson.Map(list, func(p any) any {
			if _, isObj := ojson.AsObj(p); !isObj {
				return p
			}
			return unifi.Compact("name", ojson.Get(p, "name"), "type", ojson.Get(p, "type"), "id", ojson.Get(p, "id"))
		})
	}
	return unifi.Compact(
		"time", unifi.IsoTime(ojson.Coalesce(g("time"), g("flow_start_time"))),
		"action", g("action"),
		"service", g("service"),
		"protocol", g("protocol"),
		"direction", g("direction"),
		"risk", g("risk"),
		"source", flowEndpoint(g("source")),
		"destination", flowEndpoint(g("destination")),
		"bytes_rx", ojson.Get(data, "bytes_rx"),
		"bytes_tx", ojson.Get(data, "bytes_tx"),
		"bytes_total", ojson.Get(data, "bytes_total"),
		"count", g("count"),
		"duration_ms", g("duration_milliseconds"),
		"policies", policies,
	)
}

func registerTrafficFlows(c *Context) {
	client := c.Client

	// The controller pages, searches and filters flows itself, so the listing
	// only selects fields and redacts raw flows. limit is the page size, which
	// the controller caps lower than other lists.
	flowList := listing{Limit: 50}
	c.add(Def{
		Toolset: "monitoring", Name: "unifi_list_traffic_flows", Title: "List traffic flows",
		Description: "List recent traffic flows seen by the gateway (Insights > Flows): source and destination client, IP, domain and network, app/service, allowed or blocked, risk, bytes and the policies that matched. " +
			"This is an expensive query on the gateway, so it looks back 15 minutes and returns 50 flows by default; narrow it with client_mac, action, risk or search (applied by the controller to app, domain, client name and more) before widening the window. Recent Network 9.x/10.x with a UniFi gateway.",
		Input: props(
			siteArg(),
			P("minutes", Int().Positive().Max(7*24*60).Opt().D("Look back this many minutes (default 15)")),
			P("client_mac", Str().Opt().D("Only flows from this client MAC address")),
			P("client_ip", Str().Opt().D("Only flows from this source IP")),
			P("action", Enum("allowed", "blocked").Opt().D("Only allowed or only blocked flows")),
			P("risk", Enum("low", "medium", "high").Opt().D("Only flows with this risk level")),
			P("destination_domain", Str().Opt().D("Only flows to this domain")),
			P("page", Int().Min(0).Opt().D("Page number, from 0")),
			searchArg(),
			P("limit", Int().Positive().Max(500).Opt().D("Flows per page (default 50)")),
			fieldsArg(),
			rawArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			end := nowMs()
			start := end - a.NumOr("minutes", 15)*60000
			body := ojson.New(
				"timestampFrom", start,
				"timestampTo", end,
				"pageNumber", a.NumOr("page", 0),
				"pageSize", float64(flowList.limit(a)),
				"search_text", a.Str("search"),
				"skip_count", false,
			)
			for _, k := range trafficFlowArrays {
				body.Set(k, []any{})
			}
			if a.Has("client_mac") {
				m, err := mac(a.Get("client_mac"))
				if err != nil {
					return nil, err
				}
				body.Set("source_mac", []any{m})
			}
			for arg, field := range map[string]string{"client_ip": "source_ip", "action": "action", "risk": "risk", "destination_domain": "destination_domain"} {
				if a.Has(arg) {
					body.Set(field, []any{a.Get(arg)})
				}
			}
			res, err := client.V2PostRead(ctx, c.site(a), "traffic-flows", body)
			if err != nil {
				return nil, err
			}
			page := first(res)
			flows, isList := ojson.Arr(ojson.Get(page, "data"))
			if !isList {
				flows = arr(res)
				page = ojson.New()
			}
			// The controller already applied search; don't apply it again to the summaries.
			list := flowList.result(Args{a.o.Without("search")}, flows, func(f *Obj) any { return summarizeFlow(f) }, redacted)
			out := ojson.New(
				"from", unifi.IsoTime(start),
				"to", unifi.IsoTime(end),
				"total", ojson.Coalesce(ojson.Get(page, "total_element_count"), list.Get("total")),
				"returned", list.Get("returned"),
				"has_more", ojson.OrUndefined(ojson.Get(page, "has_next")),
				"items", list.Get("items"),
				"hint", ojson.OrUndefined(list.Get("hint")),
			)
			return unifi.CompactObj(out), nil
		},
	})
}
