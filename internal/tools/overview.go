package tools

import (
	"context"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

func registerOverview(c *Context) {
	client := c.Client

	c.add(Def{
		Toolset: "overview", Name: "unifi_list_sites", Title: "List sites",
		Description: "List the UniFi sites on the controller. The `name` field is the site ID used by the `site` argument of other tools.",
		Input:       props(listing{}.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			sites, err := client.Request(ctx, "GET", "/api/self/sites", nil)
			if err != nil {
				return nil, err
			}
			return listing{}.result(a, arr(sites), func(s *Obj) any {
				return unifi.Compact("id", s.Get("_id"), "name", s.Get("name"), "description", s.Get("desc"), "role", s.Get("role"), "device_count", s.Get("device_count"))
			}, nil), nil
		},
	})

	c.add(Def{
		Toolset: "overview", Name: "unifi_get_system_info", Title: "Get controller system info",
		Description: "Get controller/Network application version, hostname, timezone, uptime and update availability.",
		Input:       props(siteArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "stat/sysinfo", nil)
			if err != nil {
				return nil, err
			}
			return ojson.Coalesce(first(res), ojson.New()), nil
		},
	})

	c.add(Def{
		Toolset: "overview", Name: "unifi_get_site_health", Title: "Get site health",
		Description: "Get the health of each subsystem (WAN, LAN, WLAN, VPN, www) for a site: status, ISP, WAN IP, latency, throughput, device and client counts.",
		Input:       props(siteArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "stat/health", nil)
			if err != nil {
				return nil, err
			}
			return mapObjs(res, func(s *Obj) any {
				rest := s.Without("gw_system-stats")
				rest.Set("gw_system_stats", s.Get("gw_system-stats"))
				return unifi.CompactObj(rest)
			}), nil
		},
	})
}
