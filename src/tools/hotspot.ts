import { z } from "zod";
import { compact, duration, isoTime, normalizeMac, redactSecrets } from "../unifi/format.js";
import { getSetting, restDelete, restList, restSave, updateSetting } from "./rest.js";
import { defineTool, defined, extraArg, idArg, macArg, siteArg, type ToolContext } from "./util.js";

export function registerHotspotTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  defineTool(ctx, "unifi_list_vouchers", {
    toolset: "hotspot",
    title: "List hotspot vouchers",
    description: "List guest hotspot vouchers with code, duration, usage and status.",
    input: { ...siteArg },
    handler: async (a) => {
      const vouchers: any[] = await client.siteGet(site(a.site), "stat/voucher");
      return vouchers.map((v) =>
        compact({
          id: v._id,
          code: typeof v.code === "string" && v.code.length === 10 ? `${v.code.slice(0, 5)}-${v.code.slice(5)}` : v.code,
          note: v.note,
          duration: duration((v.duration ?? 0) * 60),
          quota: v.quota === 0 ? "unlimited" : v.quota,
          used: v.used,
          status: v.status,
          up_kbps: v.qos_rate_max_up,
          down_kbps: v.qos_rate_max_down,
          data_limit_mb: v.qos_usage_quota,
          created: isoTime(v.create_time),
        }),
      );
    },
  });

  defineTool(ctx, "unifi_create_vouchers", {
    toolset: "hotspot",
    title: "Create hotspot vouchers",
    description: "Create guest hotspot vouchers and return their codes.",
    write: true,
    input: {
      ...siteArg,
      count: z.number().int().min(1).max(1000).optional().describe("Number of vouchers (default 1)"),
      minutes: z.number().int().positive().describe("Validity once redeemed, in minutes (e.g. 1440 = 1 day)"),
      uses: z
        .number()
        .int()
        .min(0)
        .optional()
        .describe("Times each voucher can be used: 1 = single use (default), 0 = unlimited, n = n uses"),
      note: z.string().optional().describe("Note to attach to the vouchers"),
      up_kbps: z.number().int().positive().optional().describe("Upload limit in kbps"),
      down_kbps: z.number().int().positive().optional().describe("Download limit in kbps"),
      data_limit_mb: z.number().int().positive().optional().describe("Data transfer limit in MB"),
    },
    handler: async (a) => {
      const s = site(a.site);
      const [created]: any[] = await client.cmd(s, "hotspot", "create-voucher", {
        n: a.count ?? 1,
        expire: a.minutes,
        quota: a.uses ?? 1,
        ...compact({ note: a.note, up: a.up_kbps, down: a.down_kbps, bytes: a.data_limit_mb }),
      });
      const vouchers: any[] = created?.create_time
        ? await client.sitePost(s, "stat/voucher", { create_time: created.create_time })
        : [];
      return {
        created: vouchers.length || (a.count ?? 1),
        vouchers: vouchers.map((v) =>
          compact({
            id: v._id,
            code: typeof v.code === "string" && v.code.length === 10 ? `${v.code.slice(0, 5)}-${v.code.slice(5)}` : v.code,
            minutes: v.duration,
            uses: v.quota === 0 ? "unlimited" : v.quota,
            note: v.note,
            created: isoTime(v.create_time),
          }),
        ),
      };
    },
  });

  defineTool(ctx, "unifi_revoke_voucher", {
    toolset: "hotspot",
    title: "Revoke hotspot voucher",
    description: "Delete/revoke a hotspot voucher.",
    delete: true,
    input: { ...siteArg, id: idArg("Voucher") },
    handler: async (a) => {
      await client.cmd(site(a.site), "hotspot", "delete-voucher", { _id: a.id });
      return { revoked: a.id };
    },
  });

  defineTool(ctx, "unifi_authorize_guest", {
    toolset: "hotspot",
    title: "Authorize guest",
    description: "Authorize a guest client on a hotspot/guest portal network, optionally with time, bandwidth and data limits.",
    write: true,
    input: {
      ...siteArg,
      mac: macArg,
      minutes: z.number().int().positive().optional().describe("Authorization duration in minutes (default: portal setting)"),
      up_kbps: z.number().int().positive().optional().describe("Upload limit in kbps"),
      down_kbps: z.number().int().positive().optional().describe("Download limit in kbps"),
      data_limit_mb: z.number().int().positive().optional().describe("Data transfer limit in MB"),
    },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      const res = await client.cmd(site(a.site), "stamgr", "authorize-guest", {
        mac,
        ...compact({ minutes: a.minutes, up: a.up_kbps, down: a.down_kbps, bytes: a.data_limit_mb }),
      });
      return { authorized: mac, result: res };
    },
  });

  defineTool(ctx, "unifi_unauthorize_guest", {
    toolset: "hotspot",
    title: "Unauthorize guest",
    description: "Revoke a guest client's hotspot authorization.",
    write: true,
    input: { ...siteArg, mac: macArg },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "stamgr", "unauthorize-guest", { mac });
      return { unauthorized: mac };
    },
  });

  // ---------------------------------------------------------------- operators

  defineTool(ctx, "unifi_list_hotspot_operators", {
    toolset: "hotspot",
    title: "List hotspot operators",
    description: "List hotspot operator accounts (staff who can log in to the hotspot manager to issue vouchers).",
    input: { ...siteArg },
    handler: async (a) => {
      const ops = await restList(client, site(a.site), "hotspotop");
      return ops.map((o) => compact({ id: o._id, name: o.name, note: o.note }));
    },
  });

  defineTool(ctx, "unifi_save_hotspot_operator", {
    toolset: "hotspot",
    title: "Create/update hotspot operator",
    description: "Create a hotspot operator (no id; name and password required) or update one (id; only given fields change).",
    write: true,
    input: {
      ...siteArg,
      id: idArg("Hotspot operator").optional(),
      name: z.string().min(1).optional(),
      password: z.string().min(1).optional().describe("Operator password"),
      note: z.string().optional(),
    },
    handler: async (a) => {
      if (!a.id && !(a.name && a.password)) throw new Error("name and password are required when creating an operator");
      const body = defined({ name: a.name, x_password: a.password, note: a.note });
      if (!Object.keys(body).length) throw new Error("Nothing to update");
      const op = await restSave(client, site(a.site), "hotspotop", a.id, body);
      return compact({ id: op._id ?? a.id, name: op.name, note: op.note });
    },
  });

  defineTool(ctx, "unifi_delete_hotspot_operator", {
    toolset: "hotspot",
    title: "Delete hotspot operator",
    description: "Delete a hotspot operator account.",
    delete: true,
    input: { ...siteArg, id: idArg("Hotspot operator") },
    handler: async (a) => {
      await restDelete(client, site(a.site), "hotspotop", a.id);
      return { deleted: a.id };
    },
  });

  // ---------------------------------------------------------------- guest portal

  defineTool(ctx, "unifi_get_guest_portal_settings", {
    toolset: "hotspot",
    title: "Get guest portal settings",
    description: "Get guest portal / hotspot settings: authentication method, session length, voucher/password/payment options, redirect, allowed subnets.",
    input: {
      ...siteArg,
      include_secrets: z.boolean().optional().describe("Include the portal password and payment/RADIUS secrets"),
    },
    handler: async (a) => {
      const g = await getSetting(client, site(a.site), "guest_access");
      return a.include_secrets ? g : redactSecrets(g);
    },
  });

  defineTool(ctx, "unifi_update_guest_portal_settings", {
    toolset: "hotspot",
    title: "Update guest portal settings",
    description: "Change guest portal settings. Only the fields given are changed; use extra for any other guest_access field.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      portal_enabled: z.boolean().optional().describe("Enable the guest portal"),
      auth: z
        .enum(["none", "hotspot", "password", "facebook", "custom", "radius"])
        .optional()
        .describe("Authentication: none (click-through), hotspot (vouchers/payment/etc.), password, ..."),
      session_minutes: z.number().int().positive().optional().describe("How long a guest stays authorized, in minutes"),
      password: z.string().optional().describe("Portal password (auth=password, or password option under hotspot)"),
      voucher_enabled: z.boolean().optional(),
      password_enabled: z.boolean().optional(),
      redirect_url: z.string().optional().describe("Redirect guests here after authorizing; empty string disables"),
      extra: extraArg,
    },
    handler: async (a) => {
      const patch = defined({
        portal_enabled: a.portal_enabled,
        auth: a.auth,
        expire: a.session_minutes,
        expire_number: a.session_minutes,
        expire_unit: a.session_minutes === undefined ? undefined : 1,
        x_password: a.password,
        voucher_enabled: a.voucher_enabled,
        password_enabled: a.password_enabled,
        redirect_enabled: a.redirect_url === undefined ? undefined : a.redirect_url !== "",
        redirect_url: a.redirect_url,
        ...(a.extra ?? {}),
      });
      if (!Object.keys(patch).length) throw new Error("Nothing to update");
      return redactSecrets(await updateSetting(client, site(a.site), "guest_access", patch));
    },
  });
}
