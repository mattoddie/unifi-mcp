import { z } from "zod";
import type { UnifiClient } from "../unifi/client.js";
import { compact, isoTime, normalizeMac, redactSecrets, summarizeDevice, summarizePort, summarizeRadio } from "../unifi/format.js";
import { v2Delete, v2List, v2Save } from "./rest.js";
import { defined, defineTool, extraArg, filterAndLimit, idArg, macArg, searchArg, siteArg, type ToolContext } from "./util.js";

type Obj = Record<string, any>;

const rawArg = z.boolean().optional().describe("Return the full unprocessed objects from the controller (large)");

const DEVICE_TYPE_CODES: Record<string, string[]> = {
  access_point: ["uap"],
  switch: ["usw"],
  gateway: ["ugw", "udm", "uxg"],
  other: [],
};

async function getDevice(client: UnifiClient, site: string, mac: string): Promise<Obj> {
  const [device]: Obj[] = await client.siteGet(site, `stat/device/${normalizeMac(mac)}`);
  if (!device) throw new Error(`No device with MAC ${mac} on site ${site}`);
  return device;
}

function summarizeRadioConfig(r: Obj): Obj {
  return compact({
    radio: r.radio,
    channel: r.channel,
    channel_width: r.ht,
    tx_power_mode: r.tx_power_mode,
    tx_power: r.tx_power_mode === "custom" ? r.tx_power : undefined,
    min_rssi: r.min_rssi_enabled ? r.min_rssi : undefined,
  });
}

function summarizeApGroup(g: Obj): Obj {
  return compact({
    id: g._id,
    name: g.name,
    device_macs: g.device_macs,
    device_count: Array.isArray(g.device_macs) ? g.device_macs.length : undefined,
    default: g.attr_hidden_id === "default" || g.attr_no_delete || undefined,
  });
}

/** Resolve a site name ("default") or _id to the site _id required by move-device. */
async function resolveSiteId(client: UnifiClient, siteRef: string): Promise<string> {
  if (/^[0-9a-f]{24}$/i.test(siteRef)) return siteRef;
  const sites: Obj[] = await client.request("GET", "/api/self/sites");
  const match = sites.find((s) => s.name === siteRef || s.desc === siteRef);
  if (!match) throw new Error(`No site named "${siteRef}"; available: ${sites.map((s) => `${s.name} (${s.desc})`).join(", ")}`);
  return match._id;
}

export function registerDevicesTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  // ---------------------------------------------------------------- read

  defineTool(ctx, "unifi_list_devices", {
    toolset: "devices",
    title: "List devices",
    description:
      "List UniFi network devices (gateways, switches, access points...) with state, model, firmware, uptime, load and client counts.",
    input: {
      ...siteArg,
      type: z.enum(["access_point", "switch", "gateway", "other"]).optional().describe("Only devices of this type"),
      search: searchArg,
      raw: rawArg,
    },
    handler: async (a) => {
      let devices: any[] = await client.siteGet(site(a.site), "stat/device");
      if (a.type) {
        const known = Object.values(DEVICE_TYPE_CODES).flat();
        devices = devices.filter((d) =>
          a.type === "other" ? !known.includes(d.type) : DEVICE_TYPE_CODES[a.type!].includes(d.type),
        );
      }
      const res = filterAndLimit(devices, { search: a.search }, (d) => [d.name, d.hostname, d.mac, d.ip, d.model, d.model_name]);
      return { ...res, items: a.raw ? redactSecrets(res.items) : res.items.map(summarizeDevice) };
    },
  });

  defineTool(ctx, "unifi_get_device", {
    toolset: "devices",
    title: "Get device details",
    description:
      "Get detailed information about one device by MAC: summary, switch ports, radio config and stats, uplink, LED and system stats.",
    input: { ...siteArg, mac: macArg, raw: rawArg },
    handler: async (a) => {
      const device = await getDevice(client, site(a.site), a.mac);
      if (a.raw) return redactSecrets(device);
      return compact({
        ...summarizeDevice(device),
        serial: device.serial,
        disabled: device.disabled || undefined,
        config_network: device.config_network,
        ports: device.port_table?.map(summarizePort),
        port_overrides: device.port_overrides?.length ? device.port_overrides : undefined,
        radio_config: device.radio_table?.map(summarizeRadioConfig),
        radios: device.radio_table_stats?.map(summarizeRadio),
        outlets: device.outlet_table?.map((o: Obj) =>
          compact({ index: o.index, name: o.name, relay_state: o.relay_state, cycle_enabled: o.cycle_enabled, power_w: o.outlet_power }),
        ),
        wan1: device.wan1 ? compact({ ip: device.wan1.ip, up: device.wan1.up, speed: device.wan1.speed }) : undefined,
        wan2: device.wan2 ? compact({ ip: device.wan2.ip, up: device.wan2.up, speed: device.wan2.speed }) : undefined,
        system_stats: device["system-stats"],
        load_avg: device.sys_stats
          ? [device.sys_stats.loadavg_1, device.sys_stats.loadavg_5, device.sys_stats.loadavg_15].map(Number)
          : undefined,
        total_poe_w: device.total_used_power,
        led_override: device.led_override,
        led_color: device.led_override_color,
        led_brightness: device.led_override_color_brightness,
        locating: device.locating || undefined,
      });
    },
  });

  defineTool(ctx, "unifi_get_firmware_status", {
    toolset: "devices",
    title: "Get firmware status",
    description:
      "Show each device's current firmware and whether an upgrade is available. Optionally ask the controller to refresh its firmware cache first (requires writes enabled), and/or list the firmware images it knows about.",
    input: {
      ...siteArg,
      refresh: z
        .boolean()
        .optional()
        .describe("Ask the controller to check for new device firmware first (cmd/productinfo; requires UNIFI_ALLOW_WRITES=true)"),
      include_available: z.boolean().optional().describe("Also list firmware images available to the controller (cmd/firmware list-available)"),
    },
    handler: async (a) => {
      const s = site(a.site);
      if (a.refresh) {
        if (!ctx.allowWrites) throw new Error("refresh requires UNIFI_ALLOW_WRITES=true");
        await client.sitePost(s, "cmd/productinfo", { cmd: "check-firmware-update" });
      }
      const devices: Obj[] = await client.siteGet(s, "stat/device");
      const available = a.include_available
        ? await client.cmd<Obj[]>(s, "firmware", "list-available").catch((err: Error) => ({ error: err.message }))
        : undefined;
      return compact({
        upgradable_count: devices.filter((d) => d.upgradable).length,
        devices: devices.map((d) =>
          compact({
            name: d.name ?? d.hostname,
            mac: d.mac,
            model: d.model_name ?? d.model,
            firmware: d.version,
            upgradable: Boolean(d.upgradable),
            upgrade_to_firmware: d.upgradable ? d.upgrade_to_firmware : undefined,
            upgrading: d.state === 4 || undefined,
          }),
        ),
        available,
      });
    },
  });

  defineTool(ctx, "unifi_get_rf_scan", {
    toolset: "devices",
    title: "Get RF scan results",
    description:
      "Get the state and results of an access point's RF/spectrum scan (channel utilization and interference per channel). Start one with unifi_run_rf_scan.",
    input: { ...siteArg, mac: macArg.describe("MAC address of the access point") },
    handler: async (a) => {
      const [res]: Obj[] = await client.siteGet(site(a.site), `stat/spectrum-scan/${normalizeMac(a.mac)}`);
      if (!res) return { scanning: false, results: [] };
      const { spectrum_table, spectrum_scanning, spectrum_scan_last_ts, ...rest } = res;
      return compact({
        scanning: spectrum_scanning,
        last_scan: isoTime(spectrum_scan_last_ts),
        results: Array.isArray(spectrum_table) ? spectrum_table.map((e: Obj) => compact(e)) : undefined,
        ...(spectrum_table === undefined ? rest : {}),
      });
    },
  });

  defineTool(ctx, "unifi_list_ap_groups", {
    toolset: "devices",
    title: "List AP groups",
    description: "List access point groups (used to choose which APs broadcast each WiFi network). Network 7.x+ (v2 API).",
    input: { ...siteArg },
    handler: async (a) => (await v2List(client, site(a.site), "apgroups")).map(summarizeApGroup),
  });

  // ---------------------------------------------------------------- write

  defineTool(ctx, "unifi_update_device", {
    toolset: "devices",
    title: "Update device settings",
    description:
      "Change a device's settings: name, LED (on/off/site default, plus colour/brightness on LED-ring devices), enable/disable (disabling turns off its LED and radios; mainly supported on APs) and management IP (DHCP or static). Only the given fields change.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      mac: macArg,
      name: z.string().min(1).optional().describe("New display name"),
      led: z.enum(["on", "off", "default"]).optional().describe("LED override; default follows the site-wide LED setting"),
      led_color: z
        .string()
        .regex(/^#(?:[0-9a-fA-F]{3}){1,2}$/)
        .optional()
        .describe("LED colour as hex, e.g. #0000FF (LED-ring devices only)"),
      led_brightness: z.number().int().min(0).max(100).optional().describe("LED brightness 0-100 (LED-ring devices only)"),
      disabled: z.boolean().optional().describe("true disables the device, false re-enables it"),
      ip_config: z
        .object({
          type: z.enum(["dhcp", "static"]),
          ip: z.string().optional(),
          netmask: z.string().optional(),
          gateway: z.string().optional(),
          dns1: z.string().optional(),
          dns2: z.string().optional(),
          dns_suffix: z.string().optional(),
        })
        .optional()
        .describe("Management IP configuration; static requires ip, netmask and gateway. A wrong static IP can make the device unreachable."),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      const device = await getDevice(client, s, a.mac);
      const body: Obj = defined({
        name: a.name,
        led_override: a.led,
        led_override_color: a.led_color,
        led_override_color_brightness: a.led_brightness,
        disabled: a.disabled,
      });
      if (a.ip_config) {
        const { type, ...rest } = a.ip_config;
        if (type === "static" && !(rest.ip && rest.netmask && rest.gateway)) {
          throw new Error("A static ip_config needs ip, netmask and gateway");
        }
        body.config_network = type === "dhcp" ? { type: "dhcp" } : { type: "static", ...defined(rest) };
      }
      Object.assign(body, a.extra);
      if (Object.keys(body).length === 0) throw new Error("Nothing to update");
      await client.sitePut(s, `rest/device/${device._id}`, body);
      return { mac: device.mac, updated: body };
    },
  });

  defineTool(ctx, "unifi_set_radio", {
    toolset: "devices",
    title: "Configure AP radio",
    description:
      "Change one radio on an access point: channel, channel width, transmit power and minimum RSSI. Only the given fields change. Clients on that radio will briefly disconnect.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      mac: macArg.describe("MAC address of the access point"),
      radio: z.enum(["ng", "na", "6e", "ad"]).describe("Radio: ng = 2.4 GHz, na = 5 GHz, 6e = 6 GHz, ad = 60 GHz"),
      channel: z.union([z.literal("auto"), z.number().int().positive()]).optional().describe("'auto' or a channel number"),
      channel_width: z
        .union([z.literal(20), z.literal(40), z.literal(80), z.literal(160), z.literal(240), z.literal(320)])
        .optional()
        .describe("Channel width in MHz"),
      tx_power_mode: z.enum(["auto", "high", "medium", "low", "custom"]).optional().describe("Transmit power mode"),
      tx_power: z.number().int().optional().describe("Transmit power in dBm (sets tx_power_mode=custom)"),
      min_rssi: z
        .number()
        .int()
        .max(-40)
        .nullable()
        .optional()
        .describe("Minimum RSSI in dBm (e.g. -75) below which clients are kicked; null disables it"),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      const device = await getDevice(client, s, a.mac);
      const table: Obj[] = structuredClone(device.radio_table ?? []);
      const radio = table.find((r) => r.radio === a.radio);
      if (!radio) {
        throw new Error(`${device.name ?? device.mac} has no ${a.radio} radio; radios: ${table.map((r) => r.radio).join(", ") || "none"}`);
      }
      const patch: Obj = defined({
        channel: a.channel,
        ht: a.channel_width,
        tx_power_mode: a.tx_power !== undefined ? "custom" : a.tx_power_mode,
        tx_power: a.tx_power,
      });
      if (a.min_rssi === null) patch.min_rssi_enabled = false;
      else if (a.min_rssi !== undefined) Object.assign(patch, { min_rssi_enabled: true, min_rssi: a.min_rssi });
      Object.assign(patch, a.extra);
      if (Object.keys(patch).length === 0) throw new Error("Nothing to change");
      Object.assign(radio, patch);
      await client.sitePut(s, `rest/device/${device._id}`, { radio_table: table });
      return { mac: device.mac, radio: summarizeRadioConfig(radio) };
    },
  });

  defineTool(ctx, "unifi_set_outlet", {
    toolset: "devices",
    title: "Control power outlet",
    description:
      "Switch an outlet on a UniFi SmartPower device (USP plug/strip/PDU) on or off, toggle its auto power-cycle (modem reset), or rename it. Only the given fields change.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      mac: macArg.describe("MAC address of the SmartPower device"),
      index: z.number().int().positive().describe("Outlet index (see unifi_get_device outlets[].index)"),
      on: z.boolean().optional().describe("Relay state: true = power on, false = power off"),
      cycle_enabled: z.boolean().optional().describe("Automatically power-cycle the outlet when the internet connection drops"),
      name: z.string().optional().describe("Outlet name"),
    },
    handler: async (a) => {
      const s = site(a.site);
      const device = await getDevice(client, s, a.mac);
      const outlet = (device.outlet_table ?? []).find((o: Obj) => o.index === a.index);
      if (!outlet) throw new Error(`${device.name ?? device.mac} has no outlet ${a.index}`);
      const patch = defined({ relay_state: a.on, cycle_enabled: a.cycle_enabled, name: a.name });
      if (Object.keys(patch).length === 0) throw new Error("Nothing to change");
      const overrides: Obj[] = structuredClone(device.outlet_overrides ?? []);
      let override = overrides.find((o) => o.index === a.index);
      if (override) Object.assign(override, patch);
      else {
        override = { index: a.index, ...defined({ name: outlet.name, relay_state: outlet.relay_state, cycle_enabled: outlet.cycle_enabled }), ...patch };
        overrides.push(override);
      }
      await client.sitePut(s, `rest/device/${device._id}`, { outlet_overrides: overrides });
      return { mac: device.mac, outlet: override };
    },
  });

  defineTool(ctx, "unifi_restart_device", {
    toolset: "devices",
    title: "Restart device",
    description:
      "Restart a UniFi device. reboot_type=hard also power-cycles PoE ports on switches (connected PoE devices will reboot too).",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      mac: macArg,
      reboot_type: z.enum(["soft", "hard"]).optional().describe("soft (default) or hard"),
    },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "devmgr", "restart", { mac, reboot_type: a.reboot_type ?? "soft" });
      return { restarting: mac };
    },
  });

  defineTool(ctx, "unifi_locate_device", {
    toolset: "devices",
    title: "Locate device (flash LED)",
    description: "Start or stop flashing a device's LED so it can be found physically.",
    write: true,
    input: { ...siteArg, mac: macArg, enabled: z.boolean().describe("true to start flashing, false to stop") },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "devmgr", a.enabled ? "set-locate" : "unset-locate", { mac });
      return { mac, locating: a.enabled };
    },
  });

  defineTool(ctx, "unifi_upgrade_device", {
    toolset: "devices",
    title: "Upgrade device firmware",
    description:
      "Upgrade a device to the latest firmware known to the controller, or to a specific firmware image URL. The device is offline while it upgrades and reboots. A wrong custom image can brick the device.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      mac: macArg,
      firmware_url: z.string().url().optional().describe("Upgrade to this firmware image instead of the latest (advanced)"),
    },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      if (a.firmware_url) {
        await client.sitePost(site(a.site), "cmd/devmgr/upgrade-external", { url: a.firmware_url, macs: [mac] });
        return { upgrading: mac, firmware_url: a.firmware_url };
      }
      await client.cmd(site(a.site), "devmgr", "upgrade", { mac });
      return { upgrading: mac };
    },
  });

  defineTool(ctx, "unifi_rolling_upgrade", {
    toolset: "devices",
    title: "Start/cancel rolling upgrade",
    description:
      "Start a rolling (one-at-a-time) firmware upgrade of all devices of the given types to the latest firmware, or cancel one in progress.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      action: z.enum(["start", "cancel"]),
      types: z
        .array(z.enum(["uap", "usw", "ugw", "uxg"]))
        .optional()
        .describe("Device types to upgrade when starting (default all: uap, usw, ugw, uxg)"),
    },
    handler: async (a) => {
      const s = site(a.site);
      if (a.action === "cancel") {
        await client.cmd(s, "devmgr", "unset-rollupgrade");
        return { rolling_upgrade: "cancelled" };
      }
      const types = a.types ?? ["uap", "usw", "ugw", "uxg"];
      await client.sitePost(s, "cmd/devmgr/set-rollupgrade", types);
      return { rolling_upgrade: "started", types };
    },
  });

  defineTool(ctx, "unifi_provision_device", {
    toolset: "devices",
    title: "Force provision device",
    description: "Force the controller to re-push configuration to a device.",
    write: true,
    input: { ...siteArg, mac: macArg },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "devmgr", "force-provision", { mac });
      return { provisioning: mac };
    },
  });

  defineTool(ctx, "unifi_adopt_device", {
    toolset: "devices",
    title: "Adopt device",
    description: "Adopt a device that is pending adoption (see unifi_list_devices, state=pending_adoption).",
    write: true,
    input: { ...siteArg, mac: macArg },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "devmgr", "adopt", { mac });
      return { adopting: mac };
    },
  });

  defineTool(ctx, "unifi_move_device", {
    toolset: "devices",
    title: "Move device to another site",
    description: "Move an adopted device from this site to another site on the same controller.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      mac: macArg,
      target_site: z.string().describe("Destination site: its name (e.g. 'branch'), description, or 24-character _id (see unifi_list_sites)"),
    },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      const siteId = await resolveSiteId(client, a.target_site);
      await client.cmd(site(a.site), "sitemgr", "move-device", { mac, site: siteId });
      return { moved: mac, target_site_id: siteId };
    },
  });

  defineTool(ctx, "unifi_forget_device", {
    toolset: "devices",
    title: "Forget (remove) device",
    description: "Remove a device from the site. It is factory-reset when next reachable and must be re-adopted to be used again.",
    delete: true,
    input: { ...siteArg, mac: macArg },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "sitemgr", "delete-device", { mac });
      return { forgotten: mac };
    },
  });

  defineTool(ctx, "unifi_run_rf_scan", {
    toolset: "devices",
    title: "Run RF scan",
    description:
      "Start an RF/spectrum scan on an access point. Its radios go offline for a few minutes while scanning, disconnecting clients. Fetch results with unifi_get_rf_scan.",
    write: true,
    destructive: true,
    input: { ...siteArg, mac: macArg.describe("MAC address of the access point") },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "devmgr", "spectrum-scan", { mac });
      return { scanning: mac };
    },
  });

  defineTool(ctx, "unifi_save_ap_group", {
    toolset: "devices",
    title: "Create/update AP group",
    description:
      "Create an access point group (omit id) or update one (pass id). device_macs replaces the group's member list when given.",
    write: true,
    input: {
      ...siteArg,
      id: z.string().optional().describe("AP group ID to update; omit to create"),
      name: z.string().min(1).optional().describe("Group name (required when creating)"),
      device_macs: z.array(z.string()).optional().describe("MAC addresses of the APs in the group (replaces the current list)"),
    },
    handler: async (a) => {
      if (!a.id && !a.name) throw new Error("name is required when creating an AP group");
      const body: Obj = defined({ name: a.name, device_macs: a.device_macs?.map(normalizeMac) });
      if (!a.id) body.device_macs ??= [];
      if (a.id && Object.keys(body).length === 0) throw new Error("Nothing to update");
      return summarizeApGroup(await v2Save(client, site(a.site), "apgroups", a.id, body));
    },
  });

  defineTool(ctx, "unifi_delete_ap_group", {
    toolset: "devices",
    title: "Delete AP group",
    description: "Delete an access point group. WiFi networks assigned to it must be moved to another group first.",
    delete: true,
    input: { ...siteArg, id: idArg("AP group") },
    handler: async (a) => {
      await v2Delete(client, site(a.site), "apgroups", a.id);
      return { deleted: a.id };
    },
  });
}
