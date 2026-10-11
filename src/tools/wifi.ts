import { z } from "zod";
import type { UnifiClient } from "../unifi/client.js";
import { compact, normalizeMac, redactSecrets, summarizeWlan } from "../unifi/format.js";
import { restDelete, restGet, restList, restSave, v2List } from "./rest.js";
import { defined, defineTool, extraArg, idArg, siteArg, type ToolContext } from "./util.js";

const rawArg = z.boolean().optional().describe("Return the full unprocessed objects from the controller (large)");

const secretsArg = z
  .boolean()
  .optional()
  .describe("Include secret fields such as passphrases and shared keys (redacted by default)");

type Obj = Record<string, any>;

function summarize(w: Obj, includeSecrets: boolean): Obj {
  return compact({
    ...summarizeWlan(w, includeSecrets),
    wpa3: w.wpa3_support ? (w.wpa3_transition ? "transition" : "only") : undefined,
    usergroup_id: w.usergroup_id,
    radius_profile_id: w.security === "wpaeap" ? w.radiusprofile_id : undefined,
    fast_roaming: w.fast_roaming_enabled || undefined,
    bss_transition: w.bss_transition || undefined,
    min_rate_2g_kbps: w.minrate_ng_enabled ? w.minrate_ng_data_rate_kbps : undefined,
    min_rate_5g_kbps: w.minrate_na_enabled ? w.minrate_na_data_rate_kbps : undefined,
    mac_filter_list: w.mac_filter_enabled && w.mac_filter_list?.length ? w.mac_filter_list : undefined,
  });
}

/** Escape a value for a WIFI: QR payload (backslash-escape \ ; , : "). */
function qrEscape(value: string): string {
  return value.replace(/([\\;,:"])/g, "\\$1");
}

/** Defaults the controller needs on create: default bandwidth profile, LAN and AP group. */
async function createDefaults(client: UnifiClient, site: string): Promise<Obj> {
  const [groups, nets, apGroups] = await Promise.all([
    restList(client, site, "usergroup").catch(() => [] as Obj[]),
    restList(client, site, "networkconf").catch(() => [] as Obj[]),
    v2List(client, site, "apgroups").catch(() => undefined),
  ]);
  const out: Obj = {};
  const usergroup = groups.find((g) => g.attr_no_delete) ?? groups.find((g) => g.name === "Default") ?? groups[0];
  if (usergroup) out.usergroup_id = usergroup._id;
  const lan = nets.find((n) => n.purpose === "corporate" && n.attr_no_delete) ?? nets.find((n) => n.purpose === "corporate");
  if (lan) out.networkconf_id = lan._id;
  const defaultApGroup = apGroups?.find((g) => g.attr_no_delete) ?? apGroups?.find((g) => /all aps/i.test(g.name ?? ""));
  if (defaultApGroup) {
    out.ap_group_ids = [defaultApGroup._id];
    out.ap_group_mode = "all";
  } else {
    // Older controllers use WLAN groups instead of AP groups.
    const wlanGroups = await client.siteGet<Obj[]>(site, "list/wlangroup").catch(() => [] as Obj[]);
    const wg = wlanGroups.find((g) => g.attr_no_delete) ?? wlanGroups[0];
    if (wg) out.wlangroup_id = wg._id;
  }
  return out;
}

export function registerWifiTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  defineTool(ctx, "unifi_list_wlans", {
    toolset: "wifi",
    title: "List WiFi networks",
    description: "List wireless networks (SSIDs) with security, bands, linked network and enabled state.",
    input: { ...siteArg, raw: rawArg, include_secrets: secretsArg },
    handler: async (a) => {
      const wlans: any[] = await client.siteGet(site(a.site), "rest/wlanconf");
      if (a.raw) return a.include_secrets ? wlans : redactSecrets(wlans);
      return wlans.map((w) => summarize(w, Boolean(a.include_secrets)));
    },
  });

  defineTool(ctx, "unifi_save_wlan", {
    toolset: "wifi",
    title: "Create/update WiFi network",
    description:
      "Create a WiFi network/SSID (no id) or update one (id; only the fields passed change): name, password, security, WPA3, VLAN network, bands, AP groups, " +
      "guest/hidden/isolation, bandwidth profile, MAC filter, fast roaming, minimum data rates, enabled. " +
      "On create, defaults are WPA2-PSK (or open without a passphrase), 2.4+5 GHz, the default network, bandwidth profile and all APs. " +
      "Changes reconnect clients on that SSID.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: idArg("WLAN").optional().describe("WLAN ID to update; omit to create"),
      name: z.string().min(1).max(32).optional().describe("SSID (required on create)"),
      passphrase: z.string().min(8).max(63).optional().describe("WPA passphrase (8-63 characters)"),
      security: z.enum(["open", "wpapsk", "wpaeap"]).optional().describe("open, wpapsk (personal) or wpaeap (enterprise/RADIUS)"),
      wpa_mode: z.enum(["wpa2", "wpa1", "auto"]).optional().describe("WPA version for WPA2/WPA mixed (default wpa2)"),
      wpa3: z.enum(["off", "transition", "only"]).optional().describe("WPA3: off, transition (WPA2/WPA3 mixed) or only"),
      network_id: z.string().optional().describe("Network (VLAN) the SSID bridges to (see unifi_list_networks)"),
      ap_group_ids: z.array(z.string()).optional().describe("AP groups that broadcast the SSID (default: all APs)"),
      bands: z.array(z.enum(["2g", "5g", "6g"])).min(1).optional().describe("Radio bands, e.g. ['2g','5g']"),
      hidden: z.boolean().optional().describe("Hide the SSID"),
      guest: z.boolean().optional().describe("Apply guest policies (guest portal / restrictions)"),
      client_isolation: z.boolean().optional().describe("Block clients on this SSID from talking to each other"),
      bandwidth_profile_id: z.string().optional().describe("Bandwidth profile (user group) ID, see unifi_list_bandwidth_profiles"),
      radius_profile_id: z.string().optional().describe("RADIUS profile for security wpaeap, see unifi_list_radius_profiles"),
      mac_filter: z
        .object({
          policy: z.enum(["allow", "deny"]).describe("allow = only listed MACs may join; deny = listed MACs are blocked"),
          macs: z.array(z.string()),
        })
        .nullable()
        .optional()
        .describe("MAC address filter; null disables it"),
      fast_roaming: z.boolean().optional().describe("802.11r fast roaming"),
      bss_transition: z.boolean().optional().describe("802.11v BSS transition (helps steer clients between APs)"),
      min_rate_2g_kbps: z
        .number()
        .int()
        .positive()
        .nullable()
        .optional()
        .describe("Minimum 2.4 GHz data rate in kbps (e.g. 6000, 12000); null disables"),
      min_rate_5g_kbps: z.number().int().positive().nullable().optional().describe("Minimum 5 GHz data rate in kbps; null disables"),
      enabled: z.boolean().optional(),
      extra: extraArg.describe(
        "Additional raw wlanconf fields, e.g. schedule_enabled + schedule_with_duration, uapsd_enabled, group_rekey, dtim_ng/dtim_na, pmf_mode, iapp_enabled. Merged last.",
      ),
    },
    handler: async (a) => {
      const s = site(a.site);
      const existing = a.id ? await restGet(client, s, "wlanconf", a.id) : undefined;
      if (!existing && !a.name) throw new Error("name is required when creating a WLAN");

      const security = a.security ?? existing?.security ?? (a.passphrase ? "wpapsk" : "open");
      if (security === "wpapsk" && !a.passphrase && !existing?.x_passphrase) {
        throw new Error("passphrase is required for a wpapsk WLAN");
      }
      if (security === "wpaeap" && !a.radius_profile_id && !existing?.radiusprofile_id) {
        throw new Error("radius_profile_id is required for a wpaeap (enterprise) WLAN");
      }

      const body: Obj = defined({
        name: a.name,
        x_passphrase: a.passphrase,
        security: a.security,
        wpa_mode: a.wpa_mode,
        networkconf_id: a.network_id,
        ap_group_ids: a.ap_group_ids,
        ap_group_mode: a.ap_group_ids ? "groups" : undefined,
        wlan_bands: a.bands,
        hide_ssid: a.hidden,
        is_guest: a.guest,
        l2_isolation: a.client_isolation,
        usergroup_id: a.bandwidth_profile_id,
        radiusprofile_id: a.radius_profile_id,
        fast_roaming_enabled: a.fast_roaming,
        bss_transition: a.bss_transition,
        enabled: a.enabled,
      });
      if (a.wpa3) Object.assign(body, { wpa3_support: a.wpa3 !== "off", wpa3_transition: a.wpa3 === "transition" });
      if (a.mac_filter !== undefined) {
        Object.assign(
          body,
          a.mac_filter === null
            ? { mac_filter_enabled: false }
            : { mac_filter_enabled: true, mac_filter_policy: a.mac_filter.policy, mac_filter_list: a.mac_filter.macs.map(normalizeMac) },
        );
      }
      if (a.min_rate_2g_kbps !== undefined) {
        Object.assign(body, a.min_rate_2g_kbps === null ? { minrate_ng_enabled: false } : { minrate_ng_enabled: true, minrate_ng_data_rate_kbps: a.min_rate_2g_kbps });
      }
      if (a.min_rate_5g_kbps !== undefined) {
        Object.assign(body, a.min_rate_5g_kbps === null ? { minrate_na_enabled: false } : { minrate_na_enabled: true, minrate_na_data_rate_kbps: a.min_rate_5g_kbps });
      }

      if (!existing) {
        Object.assign(body, {
          ...(await createDefaults(client, s)),
          enabled: true,
          security,
          wpa_mode: "wpa2",
          wpa_enc: "ccmp",
          wlan_bands: ["2g", "5g"],
          hide_ssid: false,
          is_guest: false,
          ...body,
        });
        if (security === "open") delete body.x_passphrase;
      }
      Object.assign(body, a.extra);

      const saved = await restSave(client, s, "wlanconf", a.id, body);
      return summarize(saved, false);
    },
  });

  defineTool(ctx, "unifi_delete_wlan", {
    toolset: "wifi",
    title: "Delete WiFi network",
    description: "Delete a WiFi network (SSID). Clients connected to it are disconnected.",
    delete: true,
    input: { ...siteArg, id: idArg("WLAN") },
    handler: async (a) => {
      const s = site(a.site);
      const wlan = await restGet(client, s, "wlanconf", a.id);
      await restDelete(client, s, "wlanconf", a.id);
      return { deleted: a.id, name: wlan.name };
    },
  });

  defineTool(ctx, "unifi_get_wifi_qr", {
    toolset: "wifi",
    title: "Get WiFi join string (QR)",
    description:
      "Get the standard WIFI: join string for an SSID, which phones understand when encoded as a QR code. NOTE: this reveals the WiFi password.",
    input: { ...siteArg, id: idArg("WLAN") },
    handler: async (a) => {
      const w = await restGet(client, site(a.site), "wlanconf", a.id);
      if (w.security === "wpaeap") throw new Error("Enterprise (RADIUS) WLANs cannot be joined with a QR code");
      const type = w.security === "open" ? "nopass" : w.wpa3_support && !w.wpa3_transition ? "SAE" : "WPA";
      const parts = [`T:${type}`, `S:${qrEscape(w.name)}`];
      if (type !== "nopass") parts.push(`P:${qrEscape(w.x_passphrase ?? "")}`);
      if (w.hide_ssid) parts.push("H:true");
      return { ssid: w.name, wifi_string: `WIFI:${parts.join(";")};;` };
    },
  });
}
