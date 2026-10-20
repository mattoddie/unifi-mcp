package tools

import (
	"context"
	"fmt"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

func findClientRecord(ctx context.Context, client *unifi.Client, site, m string) (*Obj, error) {
	res, err := client.SiteGet(ctx, site, "stat/user/"+m, nil)
	if err != nil {
		res = []any{}
	}
	record := first(res)
	if !ojson.Truthy(record) {
		return nil, fmt.Errorf("Client %s is not known to the controller on site %s", m, site)
	}
	return asObj(record), nil
}

func registerClients(c *Context) {
	client := c.Client
	raw := P("raw", Bool().Opt().D("Return the full unprocessed objects from the controller (large)"))

	c.add(Def{
		Toolset: "clients", Name: "unifi_list_clients", Title: "List connected clients",
		Description: "List clients that are currently connected, with IP, network, AP/switch port, signal, rates, usage and uptime.",
		Input: props(
			siteArg(),
			P("connection", Enum("all", "wired", "wireless").Opt().D("Filter by connection type (default all)")),
			P("guests_only", Bool().Opt().D("Only guest clients")),
			searchArg(),
			limitArg(200),
			raw,
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "stat/sta", nil)
			if err != nil {
				return nil, err
			}
			clients := arr(res)
			switch a.Str("connection") {
			case "wired":
				clients = ojson.Filter(clients, func(x any) bool { return ojson.Truthy(ojson.Get(x, "is_wired")) })
			case "wireless":
				clients = ojson.Filter(clients, func(x any) bool { return !ojson.Truthy(ojson.Get(x, "is_wired")) })
			}
			if a.Truthy("guests_only") {
				clients = ojson.Filter(clients, func(x any) bool { return ojson.Truthy(ojson.Get(x, "is_guest")) })
			}
			out := filterAndLimit(clients, a.Str("search"), optLimit(a, 200), func(x *Obj) []any {
				return []any{x.Get("name"), x.Get("hostname"), x.Get("mac"), x.Get("ip"), x.Get("oui"), x.Get("essid"), x.Get("network")}
			})
			if !a.Truthy("raw") {
				out.Set("items", mapObjs(out.Get("items"), func(x *Obj) any { return unifi.SummarizeClient(x) }))
			}
			return out, nil
		},
	})

	c.add(Def{
		Toolset: "clients", Name: "unifi_list_known_clients", Title: "List known clients",
		Description: "List all clients the controller has ever seen (including offline ones), with alias, note, fixed IP and block status.",
		Input: props(
			siteArg(),
			P("filter", Enum("all", "blocked", "fixed_ip", "noted", "named").Opt().D("Only blocked clients, clients with a fixed IP, clients with a note, or clients with an alias")),
			searchArg(),
			limitArg(200),
			raw,
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "rest/user", nil)
			if err != nil {
				return nil, err
			}
			users := arr(res)
			truthy := func(k string) func(any) bool {
				return func(u any) bool { return ojson.Truthy(ojson.Get(u, k)) }
			}
			switch a.Str("filter") {
			case "blocked":
				users = ojson.Filter(users, truthy("blocked"))
			case "fixed_ip":
				users = ojson.Filter(users, truthy("use_fixedip"))
			case "noted":
				users = ojson.Filter(users, func(u any) bool { return ojson.Truthy(ojson.Get(u, "noted")) || ojson.Truthy(ojson.Get(u, "note")) })
			case "named":
				users = ojson.Filter(users, truthy("name"))
			}
			ojson.SortStable(users, func(x, y any) float64 {
				return ojson.ToNumber(ojson.Coalesce(ojson.Get(y, "last_seen"), 0.0)) - ojson.ToNumber(ojson.Coalesce(ojson.Get(x, "last_seen"), 0.0))
			})
			out := filterAndLimit(users, a.Str("search"), optLimit(a, 200), func(u *Obj) []any {
				return []any{u.Get("name"), u.Get("hostname"), u.Get("mac"), u.Get("fixed_ip"), u.Get("last_ip"), u.Get("oui"), u.Get("note")}
			})
			if !a.Truthy("raw") {
				out.Set("items", mapObjs(out.Get("items"), func(x *Obj) any { return unifi.SummarizeClient(x) }))
			}
			return out, nil
		},
	})

	c.add(Def{
		Toolset: "clients", Name: "unifi_get_client", Title: "Get client details",
		Description: "Get everything known about a client by MAC: stored settings plus live connection details if it is online.",
		Input:       props(siteArg(), macArg(), raw),
		Handler: func(ctx context.Context, a Args) (any, error) {
			m, err := mac(a.Get("mac"))
			if err != nil {
				return nil, err
			}
			s := c.site(a)
			stored, err := client.SiteGet(ctx, s, "stat/user/"+m, nil)
			if err != nil {
				stored = []any{}
			}
			all, err := client.SiteGet(ctx, s, "stat/sta", nil)
			if err != nil {
				return nil, err
			}
			active := ojson.Find(arr(all), func(x any) bool { return ojson.StrictEq(ojson.Get(x, "mac"), m) })
			record := first(stored)
			if !ojson.Truthy(record) && !ojson.Truthy(active) {
				return nil, fmt.Errorf("No client with MAC %s on site %s", m, s)
			}
			if a.Truthy("raw") {
				return ojson.New("stored", ojson.Coalesce(record, nil), "active", ojson.Coalesce(active, nil)), nil
			}
			return ojson.New("online", ojson.Truthy(active)).Assign(unifi.SummarizeClient(ojson.Spread(record, active))), nil
		},
	})

	staCmd := func(name, title, desc string, destructive bool, cmd, key string) {
		d := Def{Toolset: "clients", Name: name, Title: title, Description: desc, Gate: Write, Input: props(siteArg(), macArg())}
		if destructive {
			d.Destructive = boolPtr(true)
		}
		d.Handler = func(ctx context.Context, a Args) (any, error) {
			m, err := mac(a.Get("mac"))
			if err != nil {
				return nil, err
			}
			if _, err := client.Cmd(ctx, c.site(a), "stamgr", cmd, ojson.New("mac", m)); err != nil {
				return nil, err
			}
			return ojson.New(key, m), nil
		}
		c.add(d)
	}
	staCmd("unifi_block_client", "Block client", "Block a client from connecting to the network.", true, "block-sta", "blocked")
	staCmd("unifi_unblock_client", "Unblock client", "Unblock a previously blocked client.", true, "unblock-sta", "unblocked")
	staCmd("unifi_reconnect_client", "Reconnect (kick) client", "Disconnect a wireless client so it reconnects (useful to force a roam or re-authentication).", true, "kick-sta", "reconnected")

	c.add(Def{
		Toolset: "clients", Name: "unifi_forget_clients", Title: "Forget clients",
		Description: "Remove clients and their history (alias, note, fixed IP, statistics) from the controller.",
		Gate:        Delete,
		Input:       props(siteArg(), P("macs", Array(Str()).Min(1).D("MAC addresses to forget"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			list, err := macs(a.Arr("macs"))
			if err != nil {
				return nil, err
			}
			if _, err := client.Cmd(ctx, c.site(a), "stamgr", "forget-sta", ojson.New("macs", list)); err != nil {
				return nil, err
			}
			return ojson.New("forgotten", list), nil
		},
	})

	c.add(Def{
		Toolset: "clients", Name: "unifi_update_client", Title: "Update client settings",
		Destructive: boolPtr(true),
		Description: "Set a known client's alias (name) and note, or assign/clear a fixed IP (DHCP reservation). Only the fields given are changed.",
		Gate:        Write,
		Input: props(
			siteArg(),
			macArg(),
			P("name", Str().Opt().D("Alias to show instead of the hostname (empty string clears it)")),
			P("note", Str().Opt().D("Free-text note (empty string clears it)")),
			P("fixed_ip", Str().Opt().D("IPv4 address to reserve for this client")),
			P("network_id", Str().Opt().D("Network ID the fixed IP belongs to (defaults to the client's current network; see unifi_list_networks)")),
			P("clear_fixed_ip", Bool().Opt().D("Remove the fixed IP reservation")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			m, err := mac(a.Get("mac"))
			if err != nil {
				return nil, err
			}
			record, err := findClientRecord(ctx, client, s, m)
			if err != nil {
				return nil, err
			}
			patch := ojson.New()
			if a.Has("name") {
				patch.Set("name", a.Get("name"))
			}
			if a.Has("note") {
				patch.Set("note", a.Get("note")).Set("noted", a.Str("note") != "")
			}
			if a.Truthy("fixed_ip") && a.Truthy("clear_fixed_ip") {
				return nil, errorf("Pass either fixed_ip or clear_fixed_ip, not both")
			}
			if a.Truthy("fixed_ip") {
				networkID := ojson.Coalesce(a.Get("network_id"), record.Get("network_id"))
				if !ojson.Truthy(networkID) {
					return nil, errorf("network_id is required because the client's current network is unknown")
				}
				patch.Set("use_fixedip", true).Set("fixed_ip", a.Get("fixed_ip")).Set("network_id", networkID)
			}
			if a.Truthy("clear_fixed_ip") {
				patch.Set("use_fixedip", false)
			}
			if patch.Len() == 0 {
				return nil, errorf("Nothing to update: pass name, note, fixed_ip or clear_fixed_ip")
			}
			idPath, err := restPath("user", record.Get("_id"))
			if err != nil {
				return nil, err
			}
			res, err := client.SitePut(ctx, s, idPath, patch)
			if err != nil {
				return nil, err
			}
			updated := first(res)
			if ojson.IsNullish(updated) {
				updated = ojson.Spread(record, patch)
			}
			return unifi.SummarizeClient(asObj(updated)), nil
		},
	})
}
