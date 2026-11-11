package tools

import (
	"context"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// voucherCode formats a 10-digit code as 12345-67890.
func voucherCode(v any) any {
	if s, ok := v.(string); ok && len([]rune(s)) == 10 && len(s) == 10 {
		return s[:5] + "-" + s[5:]
	}
	return v
}

func registerHotspot(c *Context) {
	client := c.Client

	voucherList := listing{Search: []string{"code", "note", "status"}, Limit: 200}
	c.add(Def{
		Toolset: "hotspot", Name: "unifi_list_vouchers", Title: "List hotspot vouchers",
		Description: "List guest hotspot vouchers with code, duration, usage and status.",
		Input:       props(siteArg(), voucherList.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "stat/voucher", nil)
			if err != nil {
				return nil, err
			}
			return voucherList.result(a, arr(res), func(v *Obj) any {
				g := v.Get
				return unifi.Compact(
					"id", g("_id"),
					"code", voucherCode(g("code")),
					"note", g("note"),
					"duration", unifi.Duration(ojson.ToNumber(ojson.Coalesce(g("duration"), 0.0))*60),
					"quota", ojson.If(ojson.StrictEq(g("quota"), 0.0), "unlimited", g("quota")),
					"used", g("used"),
					"status", g("status"),
					"up_kbps", g("qos_rate_max_up"),
					"down_kbps", g("qos_rate_max_down"),
					"data_limit_mb", g("qos_usage_quota"),
					"created", unifi.IsoTime(g("create_time")),
				)
			}, nil), nil
		},
	})

	c.add(Def{
		Toolset: "hotspot", Name: "unifi_create_vouchers", Title: "Create hotspot vouchers",
		Destructive: boolPtr(true),
		Description: "Create guest hotspot vouchers and return their codes.",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("count", Int().Min(1).Max(1000).Opt().D("Number of vouchers (default 1)")),
			P("minutes", Int().Positive().D("Validity once redeemed, in minutes (e.g. 1440 = 1 day)")),
			P("uses", Int().Min(0).Opt().D("Times each voucher can be used: 1 = single use (default), 0 = unlimited, n = n uses")),
			P("note", Str().Opt().D("Note to attach to the vouchers")),
			P("up_kbps", Int().Positive().Opt().D("Upload limit in kbps")),
			P("down_kbps", Int().Positive().Opt().D("Download limit in kbps")),
			P("data_limit_mb", Int().Positive().Opt().D("Data transfer limit in MB")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			count := a.NumOr("count", 1)
			params := ojson.New("n", count, "expire", a.Get("minutes"), "quota", ojson.Coalesce(a.Get("uses"), 1.0)).
				Assign(unifi.Compact("note", a.Get("note"), "up", a.Get("up_kbps"), "down", a.Get("down_kbps"), "bytes", a.Get("data_limit_mb")))
			res, err := client.Cmd(ctx, s, "hotspot", "create-voucher", params)
			if err != nil {
				return nil, err
			}
			created := first(res)
			vouchers := []any{}
			if ct := ojson.Get(created, "create_time"); ojson.Truthy(ct) {
				list, err := client.SitePostRead(ctx, s, "stat/voucher", ojson.New("create_time", ct))
				if err != nil {
					return nil, err
				}
				vouchers = arr(list)
			}
			var n any = count
			if len(vouchers) > 0 {
				n = len(vouchers)
			}
			return ojson.New(
				"created", n,
				"vouchers", mapObjs(vouchers, func(v *Obj) any {
					g := v.Get
					return unifi.Compact(
						"id", g("_id"),
						"code", voucherCode(g("code")),
						"minutes", g("duration"),
						"uses", ojson.If(ojson.StrictEq(g("quota"), 0.0), "unlimited", g("quota")),
						"note", g("note"),
						"created", unifi.IsoTime(g("create_time")),
					)
				}),
			), nil
		},
	})

	c.add(Def{
		Toolset: "hotspot", Name: "unifi_revoke_voucher", Title: "Revoke hotspot voucher",
		Description: "Delete/revoke a hotspot voucher.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Voucher")),
		Handler: func(ctx context.Context, a Args) (any, error) {
			if _, err := client.Cmd(ctx, c.site(a), "hotspot", "delete-voucher", ojson.New("_id", a.Get("id"))); err != nil {
				return nil, err
			}
			return ojson.New("revoked", a.Get("id")), nil
		},
	})

	c.add(Def{
		Toolset: "hotspot", Name: "unifi_authorize_guest", Title: "Authorize guest",
		Destructive: boolPtr(true),
		Description: "Authorize a guest client on a hotspot/guest portal network, optionally with time, bandwidth and data limits.",
		Gate:        Write,
		Input: props(
			siteArg(),
			macArg(),
			P("minutes", Int().Positive().Opt().D("Authorization duration in minutes (default: portal setting)")),
			P("up_kbps", Int().Positive().Opt().D("Upload limit in kbps")),
			P("down_kbps", Int().Positive().Opt().D("Download limit in kbps")),
			P("data_limit_mb", Int().Positive().Opt().D("Data transfer limit in MB")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			m, err := mac(a.Get("mac"))
			if err != nil {
				return nil, err
			}
			params := ojson.New("mac", m).Assign(unifi.Compact("minutes", a.Get("minutes"), "up", a.Get("up_kbps"), "down", a.Get("down_kbps"), "bytes", a.Get("data_limit_mb")))
			res, err := client.Cmd(ctx, c.site(a), "stamgr", "authorize-guest", params)
			if err != nil {
				return nil, err
			}
			return ojson.New("authorized", m, "result", res), nil
		},
	})

	c.add(Def{
		Toolset: "hotspot", Name: "unifi_unauthorize_guest", Title: "Unauthorize guest",
		Destructive: boolPtr(true),
		Description: "Revoke a guest client's hotspot authorization.",
		Gate:        Write,
		Input:       props(siteArg(), macArg()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			m, err := mac(a.Get("mac"))
			if err != nil {
				return nil, err
			}
			if _, err := client.Cmd(ctx, c.site(a), "stamgr", "unauthorize-guest", ojson.New("mac", m)); err != nil {
				return nil, err
			}
			return ojson.New("unauthorized", m), nil
		},
	})

	// ---------------------------------------------------------------- operators

	c.add(Def{
		Toolset: "hotspot", Name: "unifi_list_hotspot_operators", Title: "List hotspot operators",
		Description: "List hotspot operator accounts (staff who can log in to the hotspot manager to issue vouchers).",
		Input:       props(siteArg(), listing{}.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			ops, err := restList(ctx, client, c.site(a), "hotspotop")
			if err != nil {
				return nil, err
			}
			return listing{}.result(a, ops, func(o *Obj) any {
				return unifi.Compact("id", o.Get("_id"), "name", o.Get("name"), "note", o.Get("note"))
			}, nil), nil
		},
	})

	c.add(Def{
		Toolset: "hotspot", Name: "unifi_save_hotspot_operator", Title: "Create/update hotspot operator",
		Destructive: boolPtr(true),
		Description: "Create a hotspot operator (no id; name and password required) or update one (id; only given fields change).",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("id", idSchema("Hotspot operator").Opt()),
			P("name", Str().MinLen(1).Opt()),
			P("password", Str().MinLen(1).Opt().D("Operator password")),
			P("note", Str().Opt()),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			id := a.Str("id")
			if id == "" && !(a.Truthy("name") && a.Truthy("password")) {
				return nil, errorf("name and password are required when creating an operator")
			}
			body := unifi.Defined("name", a.Get("name"), "x_password", a.Get("password"), "note", a.Get("note"))
			if body.Len() == 0 {
				return nil, errorf("Nothing to update")
			}
			op, err := restSave(ctx, client, c.site(a), "hotspotop", id, body)
			if err != nil {
				return nil, err
			}
			return unifi.Compact("id", ojson.Coalesce(op.Get("_id"), a.Get("id")), "name", op.Get("name"), "note", op.Get("note")), nil
		},
	})

	c.add(Def{
		Toolset: "hotspot", Name: "unifi_delete_hotspot_operator", Title: "Delete hotspot operator",
		Description: "Delete a hotspot operator account.",
		Gate:        Delete,
		Input:       props(siteArg(), idArg("Hotspot operator")),
		Handler:     deleteHandler(c, "hotspotop", false),
	})

	// ---------------------------------------------------------------- guest portal

	c.add(Def{
		Toolset: "hotspot", Name: "unifi_get_guest_portal_settings", Title: "Get guest portal settings",
		Description: "Get guest portal / hotspot settings: authentication method, session length, voucher/password/payment options, redirect, allowed subnets.",
		Input: props(
			siteArg(),
			c.secretsArg("Include the portal password and payment/RADIUS secrets"),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			g, err := getSetting(ctx, client, c.site(a), "guest_access")
			if err != nil {
				return nil, err
			}
			if c.includeSecrets(a) {
				return g, nil
			}
			return unifi.RedactSecrets(g), nil
		},
	})

	c.add(Def{
		Toolset: "hotspot", Name: "unifi_update_guest_portal_settings", Title: "Update guest portal settings",
		Description: "Change guest portal settings. Only the fields given are changed; use extra for any other guest_access field.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("portal_enabled", Bool().Opt().D("Enable the guest portal")),
			P("auth", Enum("none", "hotspot", "password", "facebook", "custom", "radius").Opt().D("Authentication: none (click-through), hotspot (vouchers/payment/etc.), password, ...")),
			P("session_minutes", Int().Positive().Opt().D("How long a guest stays authorized, in minutes")),
			P("password", Str().Opt().D("Portal password (auth=password, or password option under hotspot)")),
			P("voucher_enabled", Bool().Opt()),
			P("password_enabled", Bool().Opt()),
			P("redirect_url", Str().Opt().D("Redirect guests here after authorizing; empty string disables")),
			extraArg(),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			var redirectEnabled any = U
			if a.Has("redirect_url") {
				redirectEnabled = a.Str("redirect_url") != ""
			}
			patch := unifi.Defined(
				"portal_enabled", a.Get("portal_enabled"),
				"auth", a.Get("auth"),
				"expire", a.Get("session_minutes"),
				"expire_number", a.Get("session_minutes"),
				"expire_unit", ojson.If(a.Has("session_minutes"), 1.0, U),
				"x_password", a.Get("password"),
				"voucher_enabled", a.Get("voucher_enabled"),
				"password_enabled", a.Get("password_enabled"),
				"redirect_enabled", redirectEnabled,
				"redirect_url", a.Get("redirect_url"),
			).Assign(a.Get("extra"))
			if patch.Len() == 0 {
				return nil, errorf("Nothing to update")
			}
			updated, err := updateSetting(ctx, client, c.site(a), "guest_access", patch)
			if err != nil {
				return nil, err
			}
			return unifi.RedactSecrets(updated), nil
		},
	})
}
