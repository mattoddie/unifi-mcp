import { z } from "zod";
import type { UnifiClient } from "../unifi/client.js";
import { compact, normalizeMac } from "../unifi/format.js";
import { restDelete, restList, restSave } from "./rest.js";
import { defined, defineTool, extraArg, idArg, macArg, siteArg, type ToolContext } from "./util.js";

type Obj = Record<string, any>;

const poeModeArg = z
  .enum(["auto", "pasv24", "passthrough", "off"])
  .optional()
  .describe("PoE mode: auto (802.3af/at/bt), pasv24 (24V passive), passthrough, or off");
const taggedVlanArg = z
  .enum(["auto", "block_all", "custom"])
  .optional()
  .describe("Tagged VLANs: auto (allow all), block_all (native VLAN only) or custom (allow all except excluded_network_ids)");
const speedArg = z
  .union([z.literal("auto"), z.number().int().positive()])
  .optional()
  .describe("Link speed: 'auto' (autonegotiate) or a fixed speed in Mbps (10, 100, 1000, 2500, 10000...)");

/** Port settings shared by port profiles and per-port overrides, mapped to controller field names. */
const portSettingsInput = {
  native_network_id: z.string().optional().describe("Native (untagged) network ID (see unifi_list_networks)"),
  tagged_vlan_mgmt: taggedVlanArg,
  excluded_network_ids: z
    .array(z.string())
    .optional()
    .describe("Network IDs NOT to tag on the port (used with tagged_vlan_mgmt=custom)"),
  poe_mode: poeModeArg,
  speed: speedArg,
  full_duplex: z.boolean().optional().describe("Full duplex when a fixed speed is set (default true)"),
  isolation: z.boolean().optional().describe("Port isolation: block traffic to other isolated ports"),
  storm_control: z.boolean().optional().describe("Enable broadcast/multicast/unicast storm control"),
  op_mode: z
    .enum(["switch", "mirror", "aggregate"])
    .optional()
    .describe("Operation mode: switch (normal), mirror (port mirroring) or aggregate (link aggregation)"),
};

type PortSettings = {
  native_network_id?: string;
  tagged_vlan_mgmt?: string;
  excluded_network_ids?: string[];
  poe_mode?: string;
  speed?: "auto" | number;
  full_duplex?: boolean;
  isolation?: boolean;
  storm_control?: boolean;
  op_mode?: string;
};

function portSettingsBody(a: PortSettings): Obj {
  const body: Obj = defined({
    native_networkconf_id: a.native_network_id,
    tagged_vlan_mgmt: a.tagged_vlan_mgmt,
    excluded_networkconf_ids: a.excluded_network_ids,
    poe_mode: a.poe_mode,
    isolation: a.isolation,
    stormctrl_enabled: a.storm_control,
    op_mode: a.op_mode,
  });
  if (a.speed === "auto") body.autoneg = true;
  else if (a.speed !== undefined) Object.assign(body, { autoneg: false, speed: a.speed, full_duplex: a.full_duplex ?? true });
  else if (a.full_duplex !== undefined) body.full_duplex = a.full_duplex;
  return body;
}

function summarizeProfile(p: Obj): Obj {
  return compact({
    id: p._id,
    name: p.name,
    forward: p.forward,
    native_network_id: p.native_networkconf_id,
    tagged_vlan_mgmt: p.tagged_vlan_mgmt,
    excluded_network_ids: p.excluded_networkconf_ids?.length ? p.excluded_networkconf_ids : undefined,
    poe_mode: p.poe_mode,
    speed: p.autoneg === false ? p.speed : "auto",
    op_mode: p.op_mode,
    isolation: p.isolation || undefined,
    storm_control: p.stormctrl_enabled || undefined,
    port_security: p.port_security_enabled || undefined,
  });
}

async function getSwitch(client: UnifiClient, site: string, mac: string): Promise<Obj> {
  const [device]: Obj[] = await client.siteGet(site, `stat/device/${normalizeMac(mac)}`);
  if (!device) throw new Error(`No device with MAC ${mac} on site ${site}`);
  if (!Array.isArray(device.port_table) || device.port_table.length === 0) {
    throw new Error(`Device ${device.name ?? mac} has no switch ports`);
  }
  return device;
}

function findPort(device: Obj, portIdx: number): Obj {
  const port = device.port_table.find((p: Obj) => p.port_idx === portIdx);
  if (!port) {
    const valid = device.port_table.map((p: Obj) => p.port_idx).join(", ");
    throw new Error(`Port ${portIdx} does not exist on ${device.name ?? device.mac}; valid ports: ${valid}`);
  }
  return port;
}

export function registerSwitchingTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  // ---------------------------------------------------------------- port profiles

  defineTool(ctx, "unifi_list_port_profiles", {
    toolset: "switching",
    title: "List switch port profiles",
    description: "List switch port profiles (port configurations) with native/tagged networks and PoE settings.",
    input: { ...siteArg },
    handler: async (a) => (await restList(client, site(a.site), "portconf")).map(summarizeProfile),
  });

  defineTool(ctx, "unifi_save_port_profile", {
    toolset: "switching",
    title: "Create/update switch port profile",
    description:
      "Create a switch port profile (omit id) or update one (pass id; only the given fields change). Ports using the profile are reprovisioned, which can briefly interrupt traffic.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: z.string().optional().describe("Port profile ID to update; omit to create"),
      name: z.string().min(1).optional().describe("Profile name (required when creating)"),
      ...portSettingsInput,
      extra: extraArg,
    },
    handler: async (a) => {
      if (!a.id && !a.name) throw new Error("name is required when creating a port profile");
      const body = { ...defined({ name: a.name }), ...portSettingsBody(a), ...a.extra };
      if (a.id && Object.keys(body).length === 0) throw new Error("Nothing to update");
      return summarizeProfile(await restSave(client, site(a.site), "portconf", a.id, body));
    },
  });

  defineTool(ctx, "unifi_delete_port_profile", {
    toolset: "switching",
    title: "Delete switch port profile",
    description: "Delete a switch port profile. Ports using it fall back to the default profile.",
    delete: true,
    input: { ...siteArg, id: idArg("Port profile") },
    handler: async (a) => {
      await restDelete(client, site(a.site), "portconf", a.id);
      return { deleted: a.id };
    },
  });

  // ---------------------------------------------------------------- ports

  defineTool(ctx, "unifi_list_switch_ports", {
    toolset: "switching",
    title: "List switch ports",
    description:
      "List a switch's ports with link state, speed, PoE, assigned profile and any per-port overrides configured on the device.",
    input: { ...siteArg, mac: macArg.describe("MAC address of the switch") },
    handler: async (a) => {
      const s = site(a.site);
      const [device, profiles] = await Promise.all([getSwitch(client, s, a.mac), restList(client, s, "portconf").catch(() => [])]);
      const profileNames = new Map(profiles.map((p) => [p._id, p.name]));
      const overrides = new Map<number, Obj>((device.port_overrides ?? []).map((o: Obj) => [o.port_idx, o]));
      return {
        switch: compact({ name: device.name, mac: device.mac, model: device.model_name ?? device.model }),
        ports: device.port_table.map((p: Obj) => {
          const o = overrides.get(p.port_idx);
          const profileId = o?.portconf_id ?? p.portconf_id;
          return compact({
            idx: p.port_idx,
            name: p.name,
            up: p.up,
            enabled: o?.port_security_enabled && !o?.port_security_mac_address?.length ? false : p.enable,
            speed_mbps: p.up ? p.speed : undefined,
            full_duplex: p.up ? p.full_duplex : undefined,
            media: p.media,
            poe_mode: p.poe_mode,
            poe_power_w: p.poe_power ? Number(p.poe_power) : undefined,
            is_uplink: p.is_uplink || undefined,
            op_mode: p.op_mode && p.op_mode !== "switch" ? p.op_mode : undefined,
            profile_id: profileId,
            profile_name: profileId ? profileNames.get(profileId) : undefined,
            native_network_id: p.native_networkconf_id,
            stp_state: p.stp_state,
            errors: (p.rx_errors ?? 0) + (p.tx_errors ?? 0) || undefined,
            override: o ? (({ port_idx, ...rest }) => rest)(o) : undefined,
          });
        }),
      };
    },
  });

  defineTool(ctx, "unifi_set_switch_port", {
    toolset: "switching",
    title: "Configure switch port",
    description:
      "Configure one switch port via a per-port override: name, enable/disable, port profile, native/tagged VLANs, PoE, speed, isolation, storm control. Only the given fields change; other ports are untouched. Disabling uses port security with no allowed MACs (blocks all traffic), as the UniFi UI does.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      mac: macArg.describe("MAC address of the switch"),
      port_idx: z.number().int().positive().describe("Port number (see unifi_list_switch_ports)"),
      name: z.string().optional().describe("Port name/label"),
      enabled: z.boolean().optional().describe("false blocks all traffic on the port; true re-enables it"),
      profile_id: z.string().optional().describe("Port profile ID to assign (see unifi_list_port_profiles)"),
      ...portSettingsInput,
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      const device = await getSwitch(client, s, a.mac);
      const port = findPort(device, a.port_idx);
      const patch: Obj = {
        ...defined({ name: a.name, portconf_id: a.profile_id }),
        ...portSettingsBody(a),
        ...(a.enabled !== undefined ? { port_security_enabled: !a.enabled } : {}),
        ...(a.enabled === false ? { port_security_mac_address: [] } : {}),
        ...a.extra,
      };
      if (Object.keys(patch).length === 0) throw new Error("Nothing to change: pass at least one port setting");

      const overrides: Obj[] = structuredClone(device.port_overrides ?? []);
      const existing = overrides.find((o) => o.port_idx === a.port_idx);
      let override: Obj;
      if (existing) {
        override = Object.assign(existing, patch);
      } else {
        override = { port_idx: a.port_idx, ...defined({ portconf_id: port.portconf_id }), ...patch };
        overrides.push(override);
      }
      overrides.sort((x, y) => x.port_idx - y.port_idx);
      await client.sitePut(s, `rest/device/${device._id}`, { port_overrides: overrides });
      return { switch: device.mac, port_idx: a.port_idx, override };
    },
  });

  defineTool(ctx, "unifi_reset_switch_port", {
    toolset: "switching",
    title: "Reset switch port override",
    description: "Remove the per-port override from one or more switch ports so they follow the default port profile again.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      mac: macArg.describe("MAC address of the switch"),
      port_idx: z.array(z.number().int().positive()).min(1).describe("Port numbers to reset"),
    },
    handler: async (a) => {
      const s = site(a.site);
      const device = await getSwitch(client, s, a.mac);
      a.port_idx.forEach((idx) => findPort(device, idx));
      const overrides: Obj[] = device.port_overrides ?? [];
      const kept = overrides.filter((o) => !a.port_idx.includes(o.port_idx));
      const reset = overrides.filter((o) => a.port_idx.includes(o.port_idx)).map((o) => o.port_idx);
      if (reset.length) await client.sitePut(s, `rest/device/${device._id}`, { port_overrides: kept });
      return { switch: device.mac, reset, already_default: a.port_idx.filter((i) => !reset.includes(i)) };
    },
  });

  defineTool(ctx, "unifi_power_cycle_port", {
    toolset: "switching",
    title: "Power-cycle PoE port",
    description: "Power-cycle a PoE port on a switch, rebooting the device powered by it.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      mac: macArg.describe("MAC address of the switch"),
      port_idx: z.number().int().positive().describe("Port number (see unifi_list_switch_ports)"),
    },
    handler: async (a) => {
      const mac = normalizeMac(a.mac);
      await client.cmd(site(a.site), "devmgr", "power-cycle", { mac, port_idx: a.port_idx });
      return { switch: mac, port_idx: a.port_idx, power_cycled: true };
    },
  });
}
