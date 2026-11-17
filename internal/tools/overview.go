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
		Description: "List the controller's sites with id, name, description, role and device_count. Pass a site's `name` (not its description) as the `site` argument of other tools.",
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
		Description: "Get the controller's own details (stat/sysinfo): Network application version, hostname, timezone, uptime and whether an application update is available.",
		Hints: []Hint{
			seeTool("unifi_check_updates", "To check application and device firmware updates together use unifi_check_updates."),
			seeTool("unifi_get_firmware_status", "For each device's firmware use unifi_get_firmware_status."),
		},
		Input: props(siteArg()),
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
		Description: "Get a site's status per subsystem (wan, lan, wlan, vpn, www): status, ISP, WAN IP, latency, throughput, and device and client counts. The quickest overall check of a site.",
		Hints: []Hint{
			seeTool("unifi_get_dashboard", "For WAN quality over time use unifi_get_dashboard."),
		},
		Input: props(siteArg()),
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
