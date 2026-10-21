package tools

import (
	"context"
	"math"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

var reportAttrs = map[string][]string{
	"site": {"bytes", "wan-tx_bytes", "wan-rx_bytes", "wlan_bytes", "num_sta", "lan-num_sta", "wlan-num_sta", "time"},
	"user": {"rx_bytes", "tx_bytes", "time"},
	"ap":   {"bytes", "num_sta", "time"},
	"gw":   {"time", "mem", "cpu", "loadavg_5", "lan-rx_bytes", "lan-tx_bytes", "wan-rx_bytes", "wan-tx_bytes"},
}

var defaultLookbackHours = map[string]float64{"5minutes": 12, "hourly": 24 * 7, "daily": 24 * 30, "monthly": 24 * 365}

// systemLogPayload is the request body for /v2/api/site/<site>/system-log/<class>, with the class-specific filters the UI sends.
func systemLogPayload(cls string, start, end, pageNumber, pageSize float64) *Obj {
	p := ojson.New("pageNumber", pageNumber, "pageSize", pageSize, "timestampFrom", start, "timestampTo", end)
	list := ojson.Strings
	switch cls {
	case "next-ai-alert":
		p.Set("nextAiCategory", list([]string{"CLIENT", "DEVICE", "INTERNET", "VPN"}))
	case "admin-activity":
		p.Set("activity_keys", list([]string{"ACCESSED_NETWORK_WEB", "ACCESSED_NETWORK_IOS", "ACCESSED_NETWORK_ANDROID"}))
		p.Set("change_keys", list([]string{"CLIENT", "DEVICE", "HOTSPOT", "INTERNET", "NETWORK", "PROFILE", "ROUTING", "SECURITY", "SYSTEM", "VPN", "WIFI"}))
	case "update-alert":
		p.Set("systemLogDeviceTypes", list([]string{"GATEWAYS", "SWITCHES", "ACCESS_POINT", "SMART_POWER", "BUILDING_TO_BUILDING_BRIDGES", "UNIFI_LTE"}))
	case "client-alert":
		p.Set("clientType", list([]string{"GUEST", "TELEPORT", "VPN", "WIRELESS", "RADIUS", "WIRED"}))
		p.Set("guestAuthorizationMethod", list([]string{"FACEBOOK_SOCIAL_GATEWAY", "FREE_TRIAL", "GOOGLE_SOCIAL_GATEWAY", "NONE", "PASSWORD", "PAYMENT", "RADIUS", "VOUCHER"}))
	case "threat-alert":
		p.Set("threatTypes", list([]string{"HONEYPOT", "THREAT"}))
	case "triggers":
		p.Set("triggerTypes", list([]string{"TRAFFIC_RULE", "TRAFFIC_ROUTE", "FIREWALL_RULE"}))
	}
	return p
}

func logEntries(res any) []any {
	if a, ok := ojson.Arr(res); ok {
		return a
	}
	return arr(ojson.Coalesce(ojson.Get(res, "data"), ojson.Get(res, "logs"), []any{}))
}

func summarizeLogEntry(e *Obj) *Obj {
	g := e.Get
	params := ojson.Coalesce(g("parameters"), ojson.New())
	return unifi.Compact(
		"time", unifi.IsoTime(ojson.Coalesce(g("timestamp"), g("time"))),
		"key", ojson.Coalesce(g("key"), g("type")),
		"category", g("category"),
		"severity", g("severity"),
		"title", ojson.Coalesce(ojson.Get(g("title"), "text"), g("title")),
		"message", ojson.Coalesce(ojson.Get(g("message"), "text"), g("message"), g("msg"), g("message_raw")),
		"device", ojson.Coalesce(ojson.Get(params, "DEVICE", "name"), ojson.Get(params, "DEVICE", "id")),
		"client", ojson.Coalesce(ojson.Get(params, "CLIENT", "name"), ojson.Get(params, "CLIENT", "hostname"), ojson.Get(params, "CLIENT", "id")),
	)
}

// sortDesc sorts by a numeric field, newest (largest) first; missing values count as fallback.
func sortDesc(list []any, fallback float64, fields ...string) {
	val := func(x any) float64 {
		vals := make([]any, 0, len(fields)+1)
		for _, f := range fields {
			vals = append(vals, ojson.Get(x, f))
		}
		return ojson.ToNumber(ojson.Coalesce(append(vals, fallback)...))
	}
	ojson.SortStable(list, func(x, y any) float64 { return val(y) - val(x) })
}

func limitList(list []any, limit float64) []any {
	if int(limit) < len(list) {
		return list[:int(limit)]
	}
	return list
}

func registerMonitoring(c *Context) {
	client := c.Client

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_list_events", Title: "List recent events",
		Description: "List recent controller events (client connects/roams, device restarts, upgrades...). Uses the classic event log and falls back to the v2 system log (device and client alerts) on Network versions where the classic log is gone or empty. For other log classes use unifi_get_system_log.",
		Input: props(
			siteArg(),
			P("hours", Int().Positive().Max(24*30).Opt().D("Look back this many hours (default 24)")),
			limitArg(100),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			hours := a.NumOr("hours", 24)
			limit := a.NumOr("limit", 100)
			var legacyError any = U
			res, err := client.SitePostRead(ctx, s, "stat/event", ojson.New("within", hours, "_limit", limit, "_sort", "-time"))
			if err != nil {
				legacyError = err.Error()
			} else if events := arr(res); len(events) > 0 {
				return mapObjs(events, func(e *Obj) any {
					return unifi.Compact("time", ojson.Coalesce(unifi.IsoTime(e.Get("time")), e.Get("datetime")), "key", e.Get("key"), "subsystem", e.Get("subsystem"), "message", e.Get("msg"))
				}), nil
			}
			end := nowMs()
			start := end - hours*3600000
			var entries []any
			for _, cls := range []string{"device-alert", "client-alert"} {
				r, err := client.V2PostRead(ctx, s, "system-log/"+cls, systemLogPayload(cls, start, end, 0, limit))
				if err != nil {
					continue
				}
				entries = append(entries, logEntries(r)...)
			}
			if entries == nil {
				entries = []any{}
			}
			sortDesc(entries, 0, "timestamp")
			entries = limitList(entries, limit)
			return ojson.New(
				"source", "v2 system-log (device-alert, client-alert)",
				"legacy_error", legacyError,
				"events", mapObjs(entries, func(e *Obj) any { return summarizeLogEntry(e) }),
			), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_list_alarms", Title: "List alarms",
		Description: "List controller alarms (alerts). By default only active (unarchived) alarms are returned.",
		Input: props(
			siteArg(),
			P("include_archived", Bool().Opt().D("Include archived alarms")),
			limitArg(100),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "list/alarm", nil)
			if err != nil {
				return nil, err
			}
			alarms := arr(res)
			if !a.Truthy("include_archived") {
				alarms = ojson.Filter(alarms, func(x any) bool { return !ojson.Truthy(ojson.Get(x, "archived")) })
			}
			sortDesc(alarms, 0, "time")
			return mapObjs(limitList(alarms, a.NumOr("limit", 100)), func(x *Obj) any {
				return unifi.Compact(
					"id", x.Get("_id"),
					"time", ojson.Coalesce(unifi.IsoTime(x.Get("time")), x.Get("datetime")),
					"key", x.Get("key"),
					"subsystem", x.Get("subsystem"),
					"message", x.Get("msg"),
					"archived", ojson.OrUndefined(x.Get("archived")),
				)
			}), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_archive_alarms", Title: "Archive alarms",
		Description: "Archive one alarm by ID, or all alarms when no ID is given.",
		Gate:        Write,
		Input:       props(siteArg(), P("id", Str().Opt().D("Alarm ID; omit to archive all alarms"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			var err error
			if a.Truthy("id") {
				_, err = client.Cmd(ctx, c.site(a), "evtmgr", "archive-alarm", ojson.New("_id", a.Get("id")))
			} else {
				_, err = client.Cmd(ctx, c.site(a), "evtmgr", "archive-all-alarms", nil)
			}
			if err != nil {
				return nil, err
			}
			return ojson.New("archived", ojson.Coalesce(a.Get("id"), "all")), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_get_traffic_report", Title: "Get traffic/usage report",
		Description: "Get historical time-series statistics. scope=site for whole-site traffic and client counts, gw for gateway CPU/memory/WAN traffic, ap for per-AP traffic, user for per-client traffic (pass macs).",
		Input: props(
			siteArg(),
			P("scope", Enum("site", "gw", "ap", "user").D("What to report on")),
			P("interval", Enum("5minutes", "hourly", "daily", "monthly").Opt().D("Bucket size (default hourly)")),
			P("hours", Number().Positive().Opt().D("Look back this many hours (default: 12 for 5minutes, 168 for hourly, 720 for daily, 8760 for monthly)")),
			P("macs", Array(Str()).Opt().D("Limit to these device/client MACs (ap and user scopes)")),
			P("attrs", Array(Str()).Opt().D("Override the attributes to fetch, e.g. ['rx_bytes','tx_bytes','time']")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			interval := a.Str("interval")
			if interval == "" {
				interval = "hourly"
			}
			scope := a.Str("scope")
			end := nowMs()
			start := end - a.NumOr("hours", defaultLookbackHours[interval])*3600000
			attrs := a.Arr("attrs")
			if attrs == nil {
				attrs = ojson.Strings(reportAttrs[scope])
			}
			if !ojson.Includes(attrs, "time") {
				attrs = append(attrs, "time")
			}
			body := ojson.New("attrs", attrs, "start", start, "end", end)
			if list := a.Arr("macs"); len(list) > 0 {
				norm, err := macs(list)
				if err != nil {
					return nil, err
				}
				body.Set("macs", norm)
			}
			res, err := client.SitePostRead(ctx, c.site(a), "stat/report/"+interval+"."+scope, body)
			if err != nil {
				return nil, err
			}
			return mapObjs(res, func(row *Obj) any {
				rest := row.Without("oid", "o", "site_id")
				rest.Set("time", unifi.IsoTime(rest.Get("time")))
				rest.Set("oid", ojson.If(scope == "site", U, row.Get("oid")))
				return unifi.CompactObj(rest)
			}), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_get_dpi_stats", Title: "Get DPI statistics",
		Description: "Get deep packet inspection (traffic identification) statistics, by application or by category: site-wide, or per client when macs are given. Requires traffic identification to be enabled.",
		Input: props(
			siteArg(),
			P("by", Enum("app", "category").Opt().D("Group by application or category (default category)")),
			P("macs", Array(Str()).Opt().D("Client MACs to report on instead of the whole site")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			typ := "by_cat"
			if a.Str("by") == "app" {
				typ = "by_app"
			}
			if list := a.Arr("macs"); len(list) > 0 {
				norm, err := macs(list)
				if err != nil {
					return nil, err
				}
				return client.SitePostRead(ctx, c.site(a), "stat/stadpi", ojson.New("type", typ, "macs", norm))
			}
			return client.SitePostRead(ctx, c.site(a), "stat/sitedpi", ojson.New("type", typ))
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_get_speedtest_results", Title: "Get speed test history",
		Description: "Get historical WAN speed test results run by the gateway.",
		Input: props(
			siteArg(),
			P("hours", Number().Positive().Opt().D("Look back this many hours (default 168)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			end := nowMs()
			res, err := client.SitePostRead(ctx, c.site(a), "stat/report/archive.speedtest", ojson.New(
				"attrs", ojson.Strings([]string{"xput_download", "xput_upload", "latency", "rundate", "server", "time"}),
				"start", end-a.NumOr("hours", 168)*3600000,
				"end", end,
			))
			if err != nil {
				return nil, err
			}
			return mapObjs(res, func(r *Obj) any {
				return unifi.Compact(
					"time", unifi.IsoTime(ojson.Coalesce(r.Get("time"), r.Get("rundate"))),
					"download_mbps", r.Get("xput_download"),
					"upload_mbps", r.Get("xput_upload"),
					"latency_ms", r.Get("latency"),
					"server", ojson.Coalesce(ojson.Get(r.Get("server"), "city"), ojson.Get(r.Get("server"), "name")),
				)
			}), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_run_speedtest", Title: "Run WAN speed test",
		Description: "Start a WAN speed test on the gateway. Results appear in unifi_get_speedtest_results after a minute or so.",
		Gate:        Write,
		Input:       props(siteArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			if _, err := client.Cmd(ctx, c.site(a), "devmgr", "speedtest", nil); err != nil {
				return nil, err
			}
			return ojson.New("started", true), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_list_rogue_aps", Title: "List neighbouring/rogue APs",
		Description: "List nearby access points seen by your APs (neighbours and potential rogue APs).",
		Input: props(
			siteArg(),
			P("hours", Int().Positive().Opt().D("Seen within this many hours (default 24)")),
			P("rogue_only", Bool().Opt().D("Only APs flagged as rogue")),
			limitArg(100),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SitePostRead(ctx, c.site(a), "stat/rogueap", ojson.New("within", a.NumOr("hours", 24)))
			if err != nil {
				return nil, err
			}
			aps := arr(res)
			if a.Truthy("rogue_only") {
				aps = ojson.Filter(aps, func(x any) bool { return ojson.Truthy(ojson.Get(x, "is_rogue")) })
			}
			sortDesc(aps, -999, "rssi")
			return mapObjs(limitList(aps, a.NumOr("limit", 100)), func(x *Obj) any {
				return unifi.Compact(
					"ssid", x.Get("essid"),
					"bssid", x.Get("bssid"),
					"channel", x.Get("channel"),
					"band", ojson.Coalesce(x.Get("band"), x.Get("radio")),
					"signal_dbm", x.Get("signal"),
					"rssi", x.Get("rssi"),
					"security", x.Get("security"),
					"vendor", x.Get("oui"),
					"seen_by_ap", x.Get("ap_mac"),
					"rogue", ojson.OrUndefined(x.Get("is_rogue")),
					"last_seen", unifi.IsoTime(x.Get("last_seen")),
				)
			}), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_get_system_log", Title: "Get system log",
		Description: "Read the v2 system log (Network 8+), one class at a time, paged: device-alert, client-alert, admin-activity (logins and config changes), update-alert, threat-alert (IPS/honeypot), vpn-alert, triggers (traffic/firewall rule hits), next-ai-alert.",
		Input: props(
			siteArg(),
			P("class", Enum("device-alert", "client-alert", "admin-activity", "update-alert", "threat-alert", "vpn-alert", "triggers", "next-ai-alert").Opt().D("Log class (default device-alert)")),
			P("hours", Number().Positive().Max(24*90).Opt().D("Look back this many hours (default 168)")),
			P("page", Int().Min(0).Opt().D("Page number, from 0")),
			P("page_size", Int().Min(1).Max(500).Opt().D("Entries per page (default 50)")),
			P("raw", Bool().Opt().D("Return raw entries")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			cls := a.Str("class")
			if cls == "" {
				cls = "device-alert"
			}
			end := nowMs()
			start := end - a.NumOr("hours", 168)*3600000
			page := a.NumOr("page", 0)
			res, err := client.V2PostRead(ctx, c.site(a), "system-log/"+cls, systemLogPayload(cls, start, end, page, a.NumOr("page_size", 50)))
			if err != nil {
				return nil, err
			}
			entries := logEntries(res)
			total := ojson.Coalesce(ojson.Get(res, "totalElementCount"), ojson.Get(res, "total_element_count"), ojson.Get(res, "total"))
			var shown any = entries
			if !a.Truthy("raw") {
				shown = mapObjs(entries, func(e *Obj) any { return summarizeLogEntry(e) })
			}
			return unifi.Compact("class", cls, "page", page, "total", total, "entries", shown), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_list_client_sessions", Title: "List client sessions",
		Description: "List client connection sessions (association history): when each client connected, for how long, to which AP/network, and traffic used.",
		Input: props(
			siteArg(),
			P("mac", Str().Opt().D("Only sessions for this client")),
			P("type", Enum("all", "user", "guest").Opt().D("Client type (default all)")),
			P("hours", Number().Positive().Max(24*90).Opt().D("Look back this many hours (default 168)")),
			limitArg(100),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			end := math.Floor(nowMs() / 1000)
			body := ojson.New("type", ojson.Coalesce(a.Get("type"), "all"), "start", end-ojson.Round(a.NumOr("hours", 168)*3600), "end", end)
			if a.Truthy("mac") {
				m, err := mac(a.Get("mac"))
				if err != nil {
					return nil, err
				}
				body.Set("mac", m)
			}
			res, err := client.SitePostRead(ctx, c.site(a), "stat/session", body)
			if err != nil {
				return nil, err
			}
			sessions := arr(res)
			sortDesc(sessions, 0, "assoc_time")
			return mapObjs(limitList(sessions, a.NumOr("limit", 100)), func(x *Obj) any {
				return unifi.Compact(
					"mac", x.Get("mac"),
					"name", ojson.Coalesce(x.Get("name"), x.Get("hostname")),
					"ip", x.Get("ip"),
					"connected", unifi.IsoTime(x.Get("assoc_time")),
					"disconnected", unifi.IsoTime(x.Get("disassoc_time")),
					"duration", unifi.Duration(x.Get("duration")),
					"ap_mac", x.Get("ap_mac"),
					"ssid", x.Get("essid"),
					"network", x.Get("network"),
					"guest", ojson.OrUndefined(x.Get("is_guest")),
					"rx_bytes", x.Get("rx_bytes"),
					"tx_bytes", x.Get("tx_bytes"),
				)
			}), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_list_guest_authorizations", Title: "List guest authorizations",
		Description: "List hotspot/guest portal authorizations (who was authorized, how, when and until when).",
		Input: props(
			siteArg(),
			P("hours", Number().Positive().Max(24*365).Opt().D("Look back this many hours (default 168)")),
			limitArg(100),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			end := math.Floor(nowMs() / 1000)
			res, err := client.SitePostRead(ctx, c.site(a), "stat/authorization", ojson.New("start", end-ojson.Round(a.NumOr("hours", 168)*3600), "end", end))
			if err != nil {
				return nil, err
			}
			auths := arr(res)
			sortDesc(auths, 0, "start")
			return mapObjs(limitList(auths, a.NumOr("limit", 100)), func(x *Obj) any {
				return unifi.Compact(
					"id", x.Get("_id"),
					"mac", x.Get("mac"),
					"ip", x.Get("ip"),
					"method", x.Get("authorized_by"),
					"voucher", x.Get("voucher_code"),
					"start", unifi.IsoTime(x.Get("start")),
					"end", unifi.IsoTime(x.Get("end")),
					"rx_bytes", x.Get("rx_bytes"),
					"tx_bytes", x.Get("tx_bytes"),
					"expired", ojson.OrUndefined(x.Get("expired")),
				)
			}), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_list_anomalies", Title: "List anomalies",
		Description: "List anomalies the controller detected (e.g. high latency, DNS timeouts, poor WiFi experience) per client/device. Not available on every Network version.",
		Input: props(
			siteArg(),
			P("hours", Number().Positive().Max(24*30).Opt().D("Look back this many hours (default 24)")),
			limitArg(100),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			end := nowMs()
			res, err := client.SitePostRead(ctx, c.site(a), "stat/anomalies", ojson.New("start", end-a.NumOr("hours", 24)*3600000, "end", end))
			if err != nil {
				return nil, err
			}
			// Row shape varies by version, so rows are returned as-is apart from readable timestamps.
			return mapObjs(limitList(arr(res), a.NumOr("limit", 100)), func(r *Obj) any {
				t := r.Get("time")
				out := ojson.New("time", ojson.Coalesce(unifi.IsoTime(t), t))
				out.Assign(r.Without("time"))
				return unifi.CompactObj(out)
			}), nil
		},
	})

	c.add(Def{
		Toolset: "monitoring", Name: "unifi_get_dashboard", Title: "Get dashboard metrics",
		Description: "Get the aggregated dashboard the UniFi UI shows: WAN/ISP quality history (latency, packet loss, availability, throughput), WiFi experience and client mix. v2 API, Network 7.4+; large response.",
		Input: props(
			siteArg(),
			P("hours", Number().Positive().Max(24*30).Opt().D("History window in hours (default 24)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			seconds := ojson.Round(a.NumOr("hours", 24) * 3600)
			return client.V2Query(ctx, "GET", c.site(a), "aggregated-dashboard", []unifi.Param{{Key: "historySeconds", Value: seconds}})
		},
	})
}
