package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

const secretsPasswords = "Include secret fields such as passwords and shared keys (redacted by default)"

func summarizeAdmin(a *Obj) *Obj {
	g := a.Get
	var sites any = U
	if roles, ok := ojson.Arr(g("roles")); ok {
		sites = mapObjs(roles, func(r *Obj) any { return unifi.Compact("site", r.Get("site_name"), "role", r.Get("role")) })
	}
	return unifi.Compact(
		"id", ojson.Coalesce(g("_id"), g("admin_id")),
		"name", g("name"),
		"email", g("email"),
		"role", g("role"),
		"super_admin", ojson.OrUndefined(g("is_super")),
		"owner", ojson.OrUndefined(g("is_owner")),
		"local_only", g("is_local"),
		"permissions", ojson.If(ojson.HasLength(g("permissions")), g("permissions"), U),
		"last_site", g("last_site_name"),
		"sites", sites,
		"disabled", ojson.OrUndefined(g("disabled")),
		"last_login", unifi.IsoTime(ojson.Coalesce(g("last_login_timestamp"), g("time_last_login"))),
	)
}

func registerAdmin(c *Context) {
	client := c.Client

	// ---------------------------------------------------------------- site settings

	c.add(Def{
		Toolset: "admin", Name: "unifi_get_site_settings", Title: "Get site settings",
		Description: "Get site-wide settings. Each setting is an object identified by `key`, e.g. mgmt (auto-upgrade, LEDs, SSH), country, locale, ntp, snmp, mdns (multicast DNS), rsyslogd (remote syslog), connectivity, guest_access, ips, dpi, usg (gateway: DNS, geo-IP, UPnP...), radius, network_optimization, super_mgmt (controller-wide, including auto-backup). Omit `key` to list all keys.",
		Input: props(
			siteArg(),
			P("key", Str().Opt().D("Setting key to return in full; omit to list available keys")),
			c.secretsArg(secretsPasswords),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.SiteGet(ctx, c.site(a), "rest/setting", nil)
			if err != nil {
				return nil, err
			}
			all := arr(res)
			key := a.Str("key")
			if key == "" {
				keys := ojson.Filter(ojson.Map(all, func(s any) any { return ojson.Get(s, "key") }), ojson.Truthy)
				sort.SliceStable(keys, func(i, j int) bool { return ojson.String(keys[i]) < ojson.String(keys[j]) })
				return ojson.New("keys", keys), nil
			}
			setting := ojson.Find(all, func(s any) bool { return ojson.StrictEq(ojson.Get(s, "key"), key) })
			if !ojson.Truthy(setting) {
				return nil, fmt.Errorf(`Setting "%s" not found; available: %s`, key, joinValues(ojson.Map(all, func(s any) any { return ojson.Get(s, "key") }), ", "))
			}
			if c.includeSecrets(a) {
				return setting, nil
			}
			return unifi.RedactSecrets(setting), nil
		},
	})

	c.add(Def{
		Toolset: "admin", Name: "unifi_update_site_setting", Title: "Update site setting",
		Description: "Change fields of one site setting object (see unifi_get_site_settings for keys and current field names). Only the given fields change. Examples: key=mgmt {led_enabled:false} or {auto_upgrade:true, auto_upgrade_hour:3}; key=rsyslogd {enabled:true, ip:'10.0.0.5', port:514}; key=ntp {ntp_server_1:'pool.ntp.org'}; key=snmp {enabled:true, community:'...'} (SNMPv3: enabledV3, username, x_password); key=mdns {mode:'auto'} (all, auto or custom); key=super_mgmt {autobackup_enabled:true, autobackup_cron_expr:'0 3 * * 1', autobackup_max_files:7}.",
		Gate:        Write, Destructive: boolPtr(true),
		Input: props(
			siteArg(),
			P("key", Str().D("Setting key, e.g. mgmt, ntp, rsyslogd, connectivity, usg")),
			P("changes", Record(nil).D("Fields to set, using the controller's field names")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			changes := a.Obj("changes")
			if changes.Len() == 0 {
				return nil, errorf("changes is empty")
			}
			for _, forbidden := range []string{"_id", "key", "site_id"} {
				if changes.Has(forbidden) {
					return nil, fmt.Errorf(`"%s" cannot be changed`, forbidden)
				}
			}
			updated, err := updateSetting(ctx, client, c.site(a), a.Str("key"), changes)
			if err != nil {
				return nil, err
			}
			return unifi.RedactSecrets(updated), nil
		},
	})

	// ---------------------------------------------------------------- sites

	c.add(Def{
		Toolset: "admin", Name: "unifi_create_site", Title: "Create site",
		Destructive: boolPtr(true),
		Description: "Create a new site on the controller. Returns its API name (used as `site` by other tools).",
		Gate:        Write,
		Input:       props(P("description", Str().MinLen(1).D("Display name of the new site"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.Cmd(ctx, client.DefaultSite(), "sitemgr", "add-site", ojson.New("desc", a.Get("description")))
			if err != nil {
				return nil, err
			}
			created := first(res)
			if !ojson.Truthy(created) {
				return ojson.New("created", a.Get("description")), nil
			}
			return unifi.Compact("id", ojson.Get(created, "_id"), "name", ojson.Get(created, "name"), "description", ojson.Get(created, "desc")), nil
		},
	})

	c.add(Def{
		Toolset: "admin", Name: "unifi_rename_site", Title: "Rename site",
		Description: "Change a site's display name (description). The API name stays the same.",
		Gate:        Write,
		Input:       props(siteArg(), P("description", Str().MinLen(1).D("New display name"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			if _, err := client.Cmd(ctx, c.site(a), "sitemgr", "update-site", ojson.New("desc", a.Get("description"))); err != nil {
				return nil, err
			}
			return ojson.New("site", c.site(a), "description", a.Get("description")), nil
		},
	})

	c.add(Def{
		Toolset: "admin", Name: "unifi_delete_site", Title: "Delete site",
		Description: "Delete a site and all of its configuration. Devices on it must be removed or moved first. This cannot be undone.",
		Gate:        Delete,
		Input:       props(P("site_name", Str().D("API name of the site to delete (see unifi_list_sites `name`)"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.Request(ctx, "GET", "/api/self/sites", nil)
			if err != nil {
				return nil, err
			}
			name := a.Str("site_name")
			target := ojson.Find(arr(res), func(s any) bool {
				return ojson.StrictEq(ojson.Get(s, "name"), name) || ojson.StrictEq(ojson.Get(s, "_id"), name)
			})
			if !ojson.Truthy(target) {
				return nil, fmt.Errorf("No site named %s", name)
			}
			if ojson.StrictEq(ojson.Get(target, "name"), "default") {
				return nil, errorf("The default site cannot be deleted")
			}
			// Issue the command from another site's context so we are not deleting the site we address.
			from := client.DefaultSite()
			if ojson.StrictEq(ojson.Get(target, "name"), client.DefaultSite()) {
				from = "default"
			}
			if _, err := client.Cmd(ctx, from, "sitemgr", "delete-site", ojson.New("site", ojson.Get(target, "_id"))); err != nil {
				return nil, err
			}
			return ojson.New("deleted", ojson.Get(target, "name"), "description", ojson.Get(target, "desc")), nil
		},
	})

	// ---------------------------------------------------------------- admins

	c.add(Def{
		Toolset: "admin", Name: "unifi_list_admins", Title: "List administrators",
		Description: "List administrator accounts for a site, or for all sites (all_sites: true).",
		Input:       props(siteArg(), P("all_sites", Bool().Opt().D("List admins across all sites")), listing{}.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			var admins any
			var err error
			if a.Truthy("all_sites") {
				admins, err = client.Request(ctx, "GET", "/api/stat/admin", nil)
			} else {
				admins, err = client.CmdRead(ctx, c.site(a), "sitemgr", "get-admins", nil)
			}
			if err != nil {
				return nil, err
			}
			return listing{}.result(a, arr(admins), func(x *Obj) any { return summarizeAdmin(x) }, nil), nil
		},
	})

	c.add(Def{
		Toolset: "admin", Name: "unifi_invite_admin", Title: "Invite administrator",
		Destructive: boolPtr(true),
		Description: "Invite an administrator to the site by email (re-sends the invite if they already exist). On UniFi OS consoles admins are usually managed in UniFi OS instead; this may not be supported there.",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("name", Str().D("Admin's name")),
			P("email", Str().Email().D("Admin's email address")),
			P("role", Enum("admin", "readonly").Opt().D("admin (default) or readonly")),
			P("allow_adopt", Bool().Opt().D("Allow adopting devices")),
			P("allow_restart", Bool().Opt().D("Allow restarting devices")),
			P("sso", Bool().Opt().D("Allow UI SSO (cloud) login (default true)")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			permissions := []any{}
			if a.Truthy("allow_adopt") {
				permissions = append(permissions, "API_DEVICE_ADOPT")
			}
			if a.Truthy("allow_restart") {
				permissions = append(permissions, "API_DEVICE_RESTART")
			}
			role := ojson.Coalesce(a.Get("role"), "admin")
			if _, err := client.Cmd(ctx, c.site(a), "sitemgr", "invite-admin", ojson.New(
				"name", strings.TrimSpace(a.Str("name")),
				"email", strings.TrimSpace(a.Str("email")),
				"for_sso", ojson.Coalesce(a.Get("sso"), true),
				"role", role,
				"permissions", permissions,
			)); err != nil {
				return nil, err
			}
			return ojson.New("invited", a.Get("email"), "role", role, "permissions", permissions), nil
		},
	})

	c.add(Def{
		Toolset: "admin", Name: "unifi_revoke_admin", Title: "Revoke administrator",
		Description: "Remove an administrator's access to the site. Super admins cannot be revoked this way.",
		Gate:        Delete,
		Input:       props(siteArg(), P("admin_id", Str().D("Admin ID (see unifi_list_admins `id`)"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			if _, err := client.Cmd(ctx, c.site(a), "sitemgr", "revoke-admin", ojson.New("admin", a.Get("admin_id"))); err != nil {
				return nil, err
			}
			return ojson.New("revoked", a.Get("admin_id")), nil
		},
	})

	// ---------------------------------------------------------------- backups & updates

	c.add(Def{
		Toolset: "admin", Name: "unifi_list_backups", Title: "List backups",
		Description: "List the controller's stored (auto) backups.",
		Input:       props(siteArg(), listing{}.args()),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.CmdRead(ctx, c.site(a), "backup", "list-backups", nil)
			if err != nil {
				return nil, err
			}
			backups := mapObjs(res, func(b *Obj) any {
				var size any = U
				if f, ok := ojson.Num(b.Get("size")); ok {
					size = ojson.Round(f/1048576*10) / 10
				}
				return unifi.Compact(
					"filename", b.Get("filename"),
					"time", ojson.Coalesce(unifi.IsoTime(b.Get("time")), b.Get("datetime")),
					"version", b.Get("version"),
					"size_mb", size,
					"type", b.Get("type"),
				)
			})
			ojson.SortStable(backups, func(x, y any) float64 {
				return float64(ojson.LocaleCompare(ojson.String(ojson.Coalesce(ojson.Get(y, "time"), "")), ojson.String(ojson.Coalesce(ojson.Get(x, "time"), ""))))
			})
			return listing{}.result(a, backups, nil, nil), nil
		},
	})

	c.add(Def{
		Toolset: "admin", Name: "unifi_create_backup", Title: "Create backup",
		Description: "Generate a controller backup and return the controller-relative download path. Generating can take a while on large installs.",
		Gate:        Write,
		Input: props(
			siteArg(),
			P("days", Int().Opt().D("Days of statistics history to include: -1 = all (default), 0 = settings only, or 7/30/90/180/365")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			res, err := client.Cmd(ctx, c.site(a), "backup", "backup", ojson.New("days", ojson.Coalesce(a.Get("days"), -1.0)))
			if err != nil {
				return nil, err
			}
			result := first(res)
			return ojson.New("created", true, "download_path", ojson.Get(result, "url"), "result", result), nil
		},
	})

	c.add(Def{
		Toolset: "admin", Name: "unifi_delete_backup", Title: "Delete backup",
		Description: "Delete a stored backup file.",
		Gate:        Delete,
		Input:       props(siteArg(), P("filename", Str().D("Backup filename (see unifi_list_backups)"))),
		Handler: func(ctx context.Context, a Args) (any, error) {
			if _, err := client.Cmd(ctx, c.site(a), "backup", "delete-backup", ojson.New("filename", a.Get("filename"))); err != nil {
				return nil, err
			}
			return ojson.New("deleted", a.Get("filename")), nil
		},
	})

	c.add(Def{
		Toolset: "admin", Name: "unifi_check_updates", Title: "Check for updates",
		Description: "Report whether a newer Network application version is available, and which devices have firmware upgrades. With refresh: true (requires writes enabled) the controller first re-checks Ubiquiti's servers for device firmware.",
		Input: props(
			siteArg(),
			P("refresh", Bool().Opt().D("Ask the controller to re-check for device firmware first")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			s := c.site(a)
			if a.Truthy("refresh") {
				if !c.AllowWrites {
					return nil, errorf("refresh requires UNIFI_ALLOW_WRITES=true")
				}
				if _, err := client.Cmd(ctx, s, "productinfo", "check-firmware-update", nil); err != nil {
					return nil, err
				}
			}
			latest, err := client.SiteGet(ctx, s, "stat/fwupdate/latest-version", nil)
			if err != nil {
				latest = []any{ojson.New("error", err.Error())}
			}
			res, err := client.SiteGet(ctx, s, "stat/device", nil)
			if err != nil {
				return nil, err
			}
			devices := arr(res)
			var controller any = U
			if l := first(latest); ojson.Truthy(l) {
				controller = unifi.RedactSecrets(l)
			}
			upToDate := 0
			withUpdates := []any{}
			for _, d := range devices {
				if ojson.Truthy(ojson.Get(d, "upgradable")) {
					withUpdates = append(withUpdates, unifi.Compact(
						"name", ojson.Coalesce(ojson.Get(d, "name"), ojson.Get(d, "hostname")),
						"mac", ojson.Get(d, "mac"),
						"model", ojson.Get(d, "model"),
						"current", ojson.Get(d, "version"),
						"available", ojson.Get(d, "upgrade_to_firmware"),
					))
				} else {
					upToDate++
				}
			}
			return ojson.New("controller", controller, "devices_with_updates", withUpdates, "devices_up_to_date", upToDate), nil
		},
	})
}
