import { z } from "zod";
import { compact, isoTime, redactSecrets } from "../unifi/format.js";
import { updateSetting } from "./rest.js";
import { defineTool, siteArg, type ToolContext } from "./util.js";

type Obj = Record<string, any>;

const secretsArg = z
  .boolean()
  .optional()
  .describe("Include secret fields such as passwords and shared keys (redacted by default)");

function summarizeAdmin(a: Obj): Obj {
  return compact({
    id: a._id ?? a.admin_id,
    name: a.name,
    email: a.email,
    role: a.role,
    super_admin: a.is_super || undefined,
    owner: a.is_owner || undefined,
    local_only: a.is_local === undefined ? undefined : a.is_local,
    permissions: a.permissions?.length ? a.permissions : undefined,
    last_site: a.last_site_name,
    sites: a.roles?.map?.((r: Obj) => compact({ site: r.site_name, role: r.role })),
    disabled: a.disabled || undefined,
    last_login: isoTime(a.last_login_timestamp ?? a.time_last_login),
  });
}

export function registerAdminTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  // ---------------------------------------------------------------- site settings

  defineTool(ctx, "unifi_get_site_settings", {
    toolset: "admin",
    title: "Get site settings",
    description:
      "Get site-wide settings. Each setting is an object identified by `key`, e.g. mgmt (auto-upgrade, LEDs, SSH), country, locale, ntp, snmp, rsyslogd (remote syslog), connectivity, guest_access, ips, dpi, usg (gateway: DNS, geo-IP, UPnP...), radius, network_optimization, super_mgmt. Omit `key` to list all keys.",
    input: {
      ...siteArg,
      key: z.string().optional().describe("Setting key to return in full; omit to list available keys"),
      include_secrets: secretsArg,
    },
    handler: async (a) => {
      const all: Obj[] = await client.siteGet(site(a.site), "rest/setting");
      if (!a.key) return { keys: all.map((s) => s.key).filter(Boolean).sort() };
      const setting = all.find((s) => s.key === a.key);
      if (!setting) throw new Error(`Setting "${a.key}" not found; available: ${all.map((s) => s.key).join(", ")}`);
      return a.include_secrets ? setting : redactSecrets(setting);
    },
  });

  defineTool(ctx, "unifi_update_site_setting", {
    toolset: "admin",
    title: "Update site setting",
    description:
      "Change fields of one site setting object (see unifi_get_site_settings for keys and current field names). Only the given fields change. Examples: key=mgmt {led_enabled:false} or {auto_upgrade:true, auto_upgrade_hour:3}; key=rsyslogd {enabled:true, ip:'10.0.0.5', port:514}; key=ntp {ntp_server_1:'pool.ntp.org'}.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      key: z.string().describe("Setting key, e.g. mgmt, ntp, rsyslogd, connectivity, usg"),
      changes: z.record(z.string(), z.any()).describe("Fields to set, using the controller's field names"),
    },
    handler: async (a) => {
      if (!Object.keys(a.changes).length) throw new Error("changes is empty");
      for (const forbidden of ["_id", "key", "site_id"]) {
        if (forbidden in a.changes) throw new Error(`"${forbidden}" cannot be changed`);
      }
      return redactSecrets(await updateSetting(client, site(a.site), a.key, a.changes));
    },
  });

  // ---------------------------------------------------------------- sites

  defineTool(ctx, "unifi_create_site", {
    toolset: "admin",
    title: "Create site",
    description: "Create a new site on the controller. Returns its API name (used as `site` by other tools).",
    write: true,
    input: { description: z.string().min(1).describe("Display name of the new site") },
    handler: async (a) => {
      const [created]: Obj[] = await client.cmd(client.defaultSite, "sitemgr", "add-site", { desc: a.description });
      return created ? compact({ id: created._id, name: created.name, description: created.desc }) : { created: a.description };
    },
  });

  defineTool(ctx, "unifi_rename_site", {
    toolset: "admin",
    title: "Rename site",
    description: "Change a site's display name (description). The API name stays the same.",
    write: true,
    input: { ...siteArg, description: z.string().min(1).describe("New display name") },
    handler: async (a) => {
      await client.cmd(site(a.site), "sitemgr", "update-site", { desc: a.description });
      return { site: site(a.site), description: a.description };
    },
  });

  defineTool(ctx, "unifi_delete_site", {
    toolset: "admin",
    title: "Delete site",
    description:
      "Delete a site and all of its configuration. Devices on it must be removed or moved first. This cannot be undone.",
    delete: true,
    input: { site_name: z.string().describe("API name of the site to delete (see unifi_list_sites `name`)") },
    handler: async (a) => {
      const sites: Obj[] = await client.request("GET", "/api/self/sites");
      const target = sites.find((s) => s.name === a.site_name || s._id === a.site_name);
      if (!target) throw new Error(`No site named ${a.site_name}`);
      if (target.name === "default") throw new Error("The default site cannot be deleted");
      // Issue the command from another site's context so we are not deleting the site we address.
      const from = target.name === client.defaultSite ? "default" : client.defaultSite;
      await client.cmd(from, "sitemgr", "delete-site", { site: target._id });
      return { deleted: target.name, description: target.desc };
    },
  });

  // ---------------------------------------------------------------- admins

  defineTool(ctx, "unifi_list_admins", {
    toolset: "admin",
    title: "List administrators",
    description: "List administrator accounts for a site, or for all sites (all_sites: true).",
    input: { ...siteArg, all_sites: z.boolean().optional().describe("List admins across all sites") },
    handler: async (a) => {
      const admins: Obj[] = a.all_sites
        ? await client.request("GET", "/api/stat/admin")
        : await client.cmd(site(a.site), "sitemgr", "get-admins");
      return admins.map(summarizeAdmin);
    },
  });

  defineTool(ctx, "unifi_invite_admin", {
    toolset: "admin",
    title: "Invite administrator",
    description:
      "Invite an administrator to the site by email (re-sends the invite if they already exist). On UniFi OS consoles admins are usually managed in UniFi OS instead; this may not be supported there.",
    write: true,
    input: {
      ...siteArg,
      name: z.string().describe("Admin's name"),
      email: z.string().email().describe("Admin's email address"),
      role: z.enum(["admin", "readonly"]).optional().describe("admin (default) or readonly"),
      allow_adopt: z.boolean().optional().describe("Allow adopting devices"),
      allow_restart: z.boolean().optional().describe("Allow restarting devices"),
      sso: z.boolean().optional().describe("Allow UI SSO (cloud) login (default true)"),
    },
    handler: async (a) => {
      const permissions = [a.allow_adopt && "API_DEVICE_ADOPT", a.allow_restart && "API_DEVICE_RESTART"].filter(Boolean);
      await client.cmd(site(a.site), "sitemgr", "invite-admin", {
        name: a.name.trim(),
        email: a.email.trim(),
        for_sso: a.sso ?? true,
        role: a.role ?? "admin",
        permissions,
      });
      return { invited: a.email, role: a.role ?? "admin", permissions };
    },
  });

  defineTool(ctx, "unifi_revoke_admin", {
    toolset: "admin",
    title: "Revoke administrator",
    description: "Remove an administrator's access to the site. Super admins cannot be revoked this way.",
    delete: true,
    input: { ...siteArg, admin_id: z.string().describe("Admin ID (see unifi_list_admins `id`)") },
    handler: async (a) => {
      await client.cmd(site(a.site), "sitemgr", "revoke-admin", { admin: a.admin_id });
      return { revoked: a.admin_id };
    },
  });

  // ---------------------------------------------------------------- backups & updates

  defineTool(ctx, "unifi_list_backups", {
    toolset: "admin",
    title: "List backups",
    description: "List the controller's stored (auto) backups.",
    input: { ...siteArg },
    handler: async (a) => {
      const backups: Obj[] = await client.cmd(site(a.site), "backup", "list-backups");
      return backups
        .map((b) =>
          compact({
            filename: b.filename,
            time: isoTime(b.time) ?? b.datetime,
            version: b.version,
            size_mb: typeof b.size === "number" ? Math.round((b.size / 1048576) * 10) / 10 : undefined,
            type: b.type,
          }),
        )
        .sort((x: Obj, y: Obj) => String(y.time ?? "").localeCompare(String(x.time ?? "")));
    },
  });

  defineTool(ctx, "unifi_create_backup", {
    toolset: "admin",
    title: "Create backup",
    description:
      "Generate a controller backup and return the controller-relative download path. Generating can take a while on large installs.",
    write: true,
    input: {
      ...siteArg,
      days: z
        .number()
        .int()
        .optional()
        .describe("Days of statistics history to include: -1 = all (default), 0 = settings only, or 7/30/90/180/365"),
    },
    handler: async (a) => {
      const res: Obj[] = await client.cmd(site(a.site), "backup", "backup", { days: a.days ?? -1 });
      return { created: true, download_path: res?.[0]?.url, result: res?.[0] };
    },
  });

  defineTool(ctx, "unifi_delete_backup", {
    toolset: "admin",
    title: "Delete backup",
    description: "Delete a stored backup file.",
    delete: true,
    input: { ...siteArg, filename: z.string().describe("Backup filename (see unifi_list_backups)") },
    handler: async (a) => {
      await client.cmd(site(a.site), "backup", "delete-backup", { filename: a.filename });
      return { deleted: a.filename };
    },
  });

  defineTool(ctx, "unifi_check_updates", {
    toolset: "admin",
    title: "Check for updates",
    description:
      "Report whether a newer Network application version is available, and which devices have firmware upgrades. With refresh: true (requires writes enabled) the controller first re-checks Ubiquiti's servers for device firmware.",
    input: {
      ...siteArg,
      refresh: z.boolean().optional().describe("Ask the controller to re-check for device firmware first"),
    },
    handler: async (a) => {
      const s = site(a.site);
      if (a.refresh) {
        if (!ctx.allowWrites) throw new Error("refresh requires UNIFI_ALLOW_WRITES=true");
        await client.cmd(s, "productinfo", "check-firmware-update");
      }
      const [latest, devices] = await Promise.all([
        client.siteGet<Obj[]>(s, "stat/fwupdate/latest-version").catch((err: Error) => [{ error: err.message }]),
        client.siteGet<Obj[]>(s, "stat/device"),
      ]);
      return {
        controller: latest?.[0] ? redactSecrets(latest[0]) : undefined,
        devices_with_updates: devices
          .filter((d) => d.upgradable)
          .map((d) => compact({ name: d.name ?? d.hostname, mac: d.mac, model: d.model, current: d.version, available: d.upgrade_to_firmware })),
        devices_up_to_date: devices.filter((d) => !d.upgradable).length,
      };
    },
  });
}
