import { z } from "zod";
import type { UnifiClient } from "../unifi/client.js";
import { compact, redactSecrets, summarizeNetwork } from "../unifi/format.js";
import { restDelete, restGet, restList, restSave, v2Delete, v2List, v2Save } from "./rest.js";
import { defined, defineTool, extraArg, idArg, siteArg, type ToolContext } from "./util.js";

const rawArg = z.boolean().optional().describe("Return the full unprocessed objects from the controller (large)");

const secretsArg = z
  .boolean()
  .optional()
  .describe("Include secret fields such as passphrases and shared keys (redacted by default)");

type Obj = Record<string, any>;

/** Purposes this module's LAN tools manage; WANs and VPNs have their own tools. */
const LAN_PURPOSES = ["corporate", "guest", "vlan-only"];

/** Parse "192.168.10.1/24" into the gateway address and the first/last usable host. */
export function subnetRange(subnet: string): { gateway: string; first: string; last: string } {
  const m = subnet.match(/^(\d{1,3}(?:\.\d{1,3}){3})\/(\d{1,2})$/);
  if (!m) throw new Error(`subnet must be in gateway/prefix form, e.g. 192.168.10.1/24 (got "${subnet}")`);
  const prefix = Number(m[2]);
  const octets = m[1].split(".").map(Number);
  if (octets.some((o) => o > 255) || prefix < 8 || prefix > 30) {
    throw new Error(`Invalid subnet "${subnet}" (prefix must be /8-/30)`);
  }
  const ip = octets.reduce((acc, o) => acc * 256 + o, 0);
  const size = 2 ** (32 - prefix);
  const network = Math.floor(ip / size) * size;
  const broadcast = network + size - 1;
  if (ip === network || ip === broadcast) throw new Error(`"${subnet}": the address must be the gateway IP, not the network or broadcast address`);
  const toIp = (n: number) => [24, 16, 8, 0].map((s) => Math.floor(n / 2 ** s) % 256).join(".");
  // Leave a few addresses after the gateway for static use, as the UniFi UI does;
  // fall back to the hosts on the other side of the gateway for tiny subnets.
  const [first, last] =
    ip + 5 <= broadcast - 1 ? [ip + 5, broadcast - 1] : ip + 1 <= broadcast - 1 ? [ip + 1, broadcast - 1] : [network + 1, ip - 1];
  return { gateway: m[1], first: toIp(first), last: toIp(last) };
}

function summarizeWan(n: Obj): Obj {
  return compact({
    id: n._id,
    name: n.name,
    enabled: n.enabled,
    interface: n.wan_networkgroup,
    type: n.wan_type,
    vlan: n.wan_vlan_enabled ? n.wan_vlan : undefined,
    static: n.wan_type === "static" ? compact({ ip: n.wan_ip, netmask: n.wan_netmask, gateway: n.wan_gateway }) : undefined,
    pppoe_username: n.wan_type === "pppoe" ? n.wan_username : undefined,
    dns: [n.wan_dns1, n.wan_dns2, n.wan_dns3, n.wan_dns4].filter(Boolean).length
      ? [n.wan_dns1, n.wan_dns2, n.wan_dns3, n.wan_dns4].filter(Boolean)
      : undefined,
    load_balance_type: n.wan_load_balance_type,
    load_balance_weight: n.wan_load_balance_weight,
    failover_priority: n.wan_failover_priority,
    smart_queues: n.wan_smartq_enabled
      ? compact({ download_kbps: n.wan_smartq_down_rate, upload_kbps: n.wan_smartq_up_rate })
      : undefined,
    isp_download_kbps: n.wan_provider_capabilities?.download_kilobits_per_second,
    isp_upload_kbps: n.wan_provider_capabilities?.upload_kilobits_per_second,
  });
}

function summarizeDnsRecord(r: Obj): Obj {
  return compact({
    id: r._id,
    type: r.record_type,
    name: r.key,
    value: r.value,
    ttl: r.ttl,
    enabled: r.enabled,
    port: r.port || undefined,
    priority: r.priority || undefined,
    weight: r.weight || undefined,
  });
}

function summarizeUsergroup(g: Obj): Obj {
  const rate = (v: unknown) => (typeof v === "number" && v >= 0 ? v : "unlimited");
  return compact({
    id: g._id,
    name: g.name,
    download_kbps: rate(g.qos_rate_max_down),
    upload_kbps: rate(g.qos_rate_max_up),
    default: g.attr_no_delete || undefined,
  });
}

async function findNetwork(client: UnifiClient, site: string, id: string): Promise<Obj> {
  return restGet(client, site, "networkconf", id);
}

export function registerNetworksTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  // ---------------------------------------------------------------- LANs / VLANs

  defineTool(ctx, "unifi_list_networks", {
    toolset: "networks",
    title: "List networks",
    description: "List configured networks (LANs/VLANs, WANs, VPNs) with subnet, VLAN ID and DHCP settings.",
    input: { ...siteArg, raw: rawArg, include_secrets: secretsArg },
    handler: async (a) => {
      const nets: any[] = await client.siteGet(site(a.site), "rest/networkconf");
      if (a.raw) return a.include_secrets ? nets : redactSecrets(nets);
      return nets.map(summarizeNetwork);
    },
  });

  defineTool(ctx, "unifi_save_network", {
    toolset: "networks",
    title: "Create/update LAN or VLAN",
    description:
      "Create a LAN/VLAN network (no id) or update one (id; only the fields passed change). Handles purposes corporate (regular LAN), guest and vlan-only. " +
      "On create, DHCP is enabled with a range derived from the subnet unless given. Use unifi_update_wan for WANs and unifi_save_vpn for VPNs. " +
      "Changing subnet/VLAN/DHCP disrupts connected clients.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: idArg("Network").optional().describe("Network ID to update; omit to create"),
      name: z.string().min(1).optional().describe("Network name (required on create)"),
      purpose: z.enum(["corporate", "guest", "vlan-only"]).optional().describe("corporate (default), guest, or vlan-only (VLAN with no gateway/DHCP)"),
      vlan: z.number().int().min(2).max(4095).nullable().optional().describe("VLAN ID; null removes VLAN tagging"),
      subnet: z.string().optional().describe("Gateway IP and prefix, e.g. 192.168.10.1/24"),
      dhcp_enabled: z.boolean().optional().describe("Run the DHCP server on this network"),
      dhcp_start: z.string().optional().describe("First address of the DHCP range"),
      dhcp_stop: z.string().optional().describe("Last address of the DHCP range"),
      dhcp_lease_seconds: z.number().int().positive().optional().describe("DHCP lease time in seconds (default 86400)"),
      dhcp_dns: z
        .array(z.string())
        .max(4)
        .optional()
        .describe("DNS servers handed out by DHCP (up to 4); empty array reverts to the gateway"),
      dhcp_gateway: z.string().nullable().optional().describe("Override the gateway handed out by DHCP; null reverts to automatic"),
      domain_name: z.string().optional().describe("Domain name handed out by DHCP"),
      igmp_snooping: z.boolean().optional(),
      isolated: z.boolean().optional().describe("Network isolation: block traffic to other local networks"),
      internet_access: z.boolean().optional().describe("Allow internet access (false blocks it)"),
      mdns: z.boolean().optional().describe("Enable multicast DNS forwarding for this network"),
      enabled: z.boolean().optional(),
      extra: extraArg.describe(
        "Additional raw networkconf fields, e.g. DHCP options (dhcpd_ntp_1, dhcpd_boot_enabled/dhcpd_boot_server/dhcpd_boot_filename, dhcpd_tftp_server, dhcpd_wins_1) or IPv6 (ipv6_interface_type, ipv6_ra_enabled). Merged last.",
      ),
    },
    handler: async (a) => {
      const s = site(a.site);
      let existing: Obj | undefined;
      if (a.id) {
        existing = await findNetwork(client, s, a.id);
        if (!LAN_PURPOSES.includes(existing.purpose)) {
          throw new Error(
            `Network ${a.id} has purpose "${existing.purpose}"; use ${existing.purpose === "wan" ? "unifi_update_wan" : "unifi_save_vpn"} instead`,
          );
        }
      } else if (!a.name) {
        throw new Error("name is required when creating a network");
      }

      const purpose = a.purpose ?? existing?.purpose ?? "corporate";
      const body: Obj = defined({
        name: a.name,
        purpose: a.purpose,
        domain_name: a.domain_name,
        igmp_snooping: a.igmp_snooping,
        network_isolation_enabled: a.isolated,
        internet_access_enabled: a.internet_access,
        mdns_enabled: a.mdns,
        enabled: a.enabled,
        dhcpd_enabled: a.dhcp_enabled,
        dhcpd_start: a.dhcp_start,
        dhcpd_stop: a.dhcp_stop,
        dhcpd_leasetime: a.dhcp_lease_seconds,
        ip_subnet: a.subnet,
      });

      if (a.vlan !== undefined) Object.assign(body, a.vlan === null ? { vlan_enabled: false } : { vlan_enabled: true, vlan: a.vlan });
      if (a.dhcp_dns) {
        body.dhcpd_dns_enabled = a.dhcp_dns.length > 0;
        for (let i = 0; i < 4; i++) body[`dhcpd_dns_${i + 1}`] = a.dhcp_dns[i] ?? "";
      }
      if (a.dhcp_gateway !== undefined) {
        Object.assign(body, a.dhcp_gateway === null ? { dhcpd_gateway_enabled: false } : { dhcpd_gateway_enabled: true, dhcpd_gateway: a.dhcp_gateway });
      }

      if (purpose === "vlan-only") {
        if (!existing && a.vlan == null) throw new Error("vlan is required for a vlan-only network");
        if (a.subnet || a.dhcp_enabled) throw new Error("vlan-only networks have no subnet or DHCP server");
      }

      // Derive a DHCP range when the subnet changes and no range was given.
      if (a.subnet && purpose !== "vlan-only") {
        const range = subnetRange(a.subnet);
        if (!a.dhcp_start && !a.dhcp_stop) Object.assign(body, { dhcpd_start: range.first, dhcpd_stop: range.last });
      }

      if (!existing) {
        if (purpose !== "vlan-only" && !a.subnet) throw new Error("subnet is required when creating a corporate or guest network");
        Object.assign(body, {
          purpose,
          networkgroup: "LAN",
          enabled: a.enabled ?? true,
          ...(purpose === "vlan-only"
            ? {}
            : {
                dhcpd_enabled: a.dhcp_enabled ?? true,
                dhcpd_leasetime: a.dhcp_lease_seconds ?? 86400,
                dhcpd_dns_enabled: body.dhcpd_dns_enabled ?? false,
                dhcpd_gateway_enabled: body.dhcpd_gateway_enabled ?? false,
                igmp_snooping: a.igmp_snooping ?? false,
              }),
          vlan_enabled: body.vlan_enabled ?? false,
          ...body,
        });
      }
      Object.assign(body, a.extra);

      const saved = await restSave(client, s, "networkconf", a.id, body);
      return summarizeNetwork(saved);
    },
  });

  defineTool(ctx, "unifi_delete_network", {
    toolset: "networks",
    title: "Delete LAN or VLAN",
    description:
      "Delete a LAN/VLAN network (purpose corporate, guest or vlan-only). WAN networks cannot be deleted here; VPNs use unifi_delete_vpn. Clients on the network lose connectivity.",
    delete: true,
    input: { ...siteArg, id: idArg("Network") },
    handler: async (a) => {
      const s = site(a.site);
      const net = await findNetwork(client, s, a.id);
      if (net.attr_no_delete) throw new Error(`Network "${net.name}" is a built-in network and cannot be deleted`);
      if (!LAN_PURPOSES.includes(net.purpose)) {
        throw new Error(`Network "${net.name}" has purpose "${net.purpose}"; only LAN/VLAN networks can be deleted with this tool`);
      }
      await restDelete(client, s, "networkconf", a.id);
      return { deleted: a.id, name: net.name };
    },
  });

  // ---------------------------------------------------------------- WAN

  defineTool(ctx, "unifi_list_wans", {
    toolset: "networks",
    title: "List WAN connections",
    description:
      "List WAN interfaces with connection type, VLAN, static/PPPoE settings, failover/load-balancing and Smart Queues, plus live WAN health.",
    input: { ...siteArg },
    handler: async (a) => {
      const s = site(a.site);
      const [nets, health] = await Promise.all([
        restList(client, s, "networkconf"),
        client.siteGet<Obj[]>(s, "stat/health").catch(() => [] as Obj[]),
      ]);
      const wan = health.find((h) => h.subsystem === "wan");
      return {
        wans: nets.filter((n) => n.purpose === "wan").map(summarizeWan),
        health: wan
          ? compact({
              status: wan.status,
              wan_ip: wan.wan_ip,
              isp: wan.isp_name ?? wan.isp_organization,
              gateways: wan.num_gw,
              uptime_stats: wan.uptime_stats,
            })
          : undefined,
      };
    },
  });

  defineTool(ctx, "unifi_update_wan", {
    toolset: "networks",
    title: "Update WAN settings",
    description:
      "Change a WAN interface's connection type (DHCP/static/PPPoE), VLAN, DNS, failover/load balancing or Smart Queues. Only the fields passed change. " +
      "A wrong setting can take the site offline.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: idArg("WAN network (see unifi_list_wans)"),
      type: z.enum(["dhcp", "static", "pppoe"]).optional().describe("Connection type"),
      static_ip: z.string().optional().describe("Static WAN IP (type static)"),
      netmask: z.string().optional().describe("Static WAN netmask, e.g. 255.255.255.0"),
      gateway: z.string().optional().describe("Static WAN gateway"),
      dns: z.array(z.string()).max(4).optional().describe("Manual DNS servers; empty array reverts to ISP-provided DNS"),
      pppoe_username: z.string().optional(),
      pppoe_password: z.string().optional(),
      vlan: z.number().int().min(1).max(4095).nullable().optional().describe("WAN VLAN ID (some ISPs require one); null disables"),
      load_balance_type: z
        .enum(["failover-only", "weighted"])
        .optional()
        .describe("failover-only uses this WAN only when higher-priority WANs fail; weighted load-balances"),
      load_balance_weight: z.number().int().min(1).max(99).optional().describe("Weight for weighted load balancing"),
      failover_priority: z.number().int().min(1).optional().describe("Failover priority (lower is preferred)"),
      smart_queues: z.boolean().optional().describe("Enable Smart Queues (SQM) to reduce bufferbloat"),
      download_kbps: z.number().int().positive().optional().describe("Smart Queues / ISP download rate in kbps"),
      upload_kbps: z.number().int().positive().optional().describe("Smart Queues / ISP upload rate in kbps"),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      const wan = await findNetwork(client, s, a.id);
      if (wan.purpose !== "wan") throw new Error(`Network ${a.id} ("${wan.name}") is not a WAN`);
      const type = a.type ?? wan.wan_type;
      if (a.type === "static" && !(a.static_ip ?? wan.wan_ip) ) throw new Error("static_ip is required for a static WAN");

      const body: Obj = defined({
        wan_type: a.type,
        wan_ip: a.static_ip,
        wan_netmask: a.netmask,
        wan_gateway: a.gateway,
        wan_username: a.pppoe_username,
        x_wan_password: a.pppoe_password,
        wan_load_balance_type: a.load_balance_type,
        wan_load_balance_weight: a.load_balance_weight,
        wan_failover_priority: a.failover_priority,
        wan_smartq_enabled: a.smart_queues,
        wan_smartq_down_rate: a.smart_queues !== false ? a.download_kbps : undefined,
        wan_smartq_up_rate: a.smart_queues !== false ? a.upload_kbps : undefined,
      });
      if ((a.static_ip || a.netmask || a.gateway) && type !== "static") {
        throw new Error("static_ip/netmask/gateway only apply to a static WAN (pass type: static)");
      }
      if ((a.pppoe_username || a.pppoe_password) && type !== "pppoe") {
        throw new Error("pppoe_username/pppoe_password only apply to a PPPoE WAN (pass type: pppoe)");
      }
      if (a.vlan !== undefined) Object.assign(body, a.vlan === null ? { wan_vlan_enabled: false } : { wan_vlan_enabled: true, wan_vlan: a.vlan });
      if (a.dns) {
        body.wan_dns_preference = a.dns.length ? "manual" : "auto";
        for (let i = 0; i < 4; i++) body[`wan_dns${i + 1}`] = a.dns[i] ?? "";
      }
      if (a.download_kbps || a.upload_kbps) {
        body.wan_provider_capabilities = {
          ...wan.wan_provider_capabilities,
          ...defined({ download_kilobits_per_second: a.download_kbps, upload_kilobits_per_second: a.upload_kbps }),
        };
      }
      Object.assign(body, a.extra);
      if (Object.keys(body).length === 0) throw new Error("Nothing to update");

      const saved = await restSave(client, s, "networkconf", a.id, body);
      return summarizeWan(saved);
    },
  });

  // ---------------------------------------------------------------- local DNS

  defineTool(ctx, "unifi_list_dns_records", {
    toolset: "networks",
    title: "List local DNS records",
    description: "List local DNS records served by the gateway (v2 static-dns; UniFi Network 8.2+).",
    input: { ...siteArg },
    handler: async (a) => (await v2List(client, site(a.site), "static-dns")).map(summarizeDnsRecord),
  });

  defineTool(ctx, "unifi_save_dns_record", {
    toolset: "networks",
    title: "Create/update local DNS record",
    description:
      "Create (no id) or update (id; only fields passed change) a local DNS record served by the gateway (UniFi Network 8.2+). " +
      "For A/AAAA the value is an IP; for CNAME a hostname; for MX/SRV set priority (and weight/port for SRV).",
    write: true,
    input: {
      ...siteArg,
      id: idArg("DNS record").optional().describe("Record ID to update; omit to create"),
      type: z.enum(["A", "AAAA", "CNAME", "MX", "TXT", "SRV", "NS"]).optional().describe("Record type (default A on create)"),
      name: z.string().optional().describe("Record name (domain), e.g. nas.home.arpa (required on create)"),
      value: z.string().optional().describe("Record value: IP address, target hostname or text (required on create)"),
      ttl: z.number().int().min(0).optional().describe("TTL in seconds; 0 = automatic"),
      priority: z.number().int().min(0).optional().describe("MX/SRV priority"),
      weight: z.number().int().min(0).optional().describe("SRV weight"),
      port: z.number().int().min(0).max(65535).optional().describe("SRV port"),
      enabled: z.boolean().optional(),
      extra: extraArg,
    },
    handler: async (a) => {
      if (!a.id && (!a.name || !a.value)) throw new Error("name and value are required when creating a DNS record");
      const body: Obj = defined({
        record_type: a.type,
        key: a.name,
        value: a.value,
        ttl: a.ttl,
        priority: a.priority,
        weight: a.weight,
        port: a.port,
        enabled: a.enabled,
      });
      if (!a.id) Object.assign(body, { record_type: a.type ?? "A", ttl: a.ttl ?? 0, enabled: a.enabled ?? true });
      Object.assign(body, a.extra);
      return summarizeDnsRecord(await v2Save(client, site(a.site), "static-dns", a.id, body));
    },
  });

  defineTool(ctx, "unifi_delete_dns_record", {
    toolset: "networks",
    title: "Delete local DNS record",
    description: "Delete a local DNS record.",
    delete: true,
    input: { ...siteArg, id: idArg("DNS record") },
    handler: async (a) => {
      await v2Delete(client, site(a.site), "static-dns", a.id);
      return { deleted: a.id };
    },
  });

  // ---------------------------------------------------------------- bandwidth profiles (user groups)

  defineTool(ctx, "unifi_list_bandwidth_profiles", {
    toolset: "networks",
    title: "List bandwidth profiles",
    description: "List bandwidth profiles (user groups) that cap client download/upload rates; assign them to WLANs or clients.",
    input: { ...siteArg },
    handler: async (a) => (await restList(client, site(a.site), "usergroup")).map(summarizeUsergroup),
  });

  defineTool(ctx, "unifi_save_bandwidth_profile", {
    toolset: "networks",
    title: "Create/update bandwidth profile",
    description: "Create (no id) or update (id) a bandwidth profile (user group). Rates are in kbps; -1 means unlimited.",
    write: true,
    input: {
      ...siteArg,
      id: idArg("Bandwidth profile").optional().describe("Profile ID to update; omit to create"),
      name: z.string().min(1).optional().describe("Profile name (required on create)"),
      download_kbps: z.number().int().min(-1).optional().describe("Download cap in kbps (-1 = unlimited)"),
      upload_kbps: z.number().int().min(-1).optional().describe("Upload cap in kbps (-1 = unlimited)"),
    },
    handler: async (a) => {
      if (!a.id && !a.name) throw new Error("name is required when creating a bandwidth profile");
      const body: Obj = defined({ name: a.name, qos_rate_max_down: a.download_kbps, qos_rate_max_up: a.upload_kbps });
      if (!a.id) Object.assign(body, { qos_rate_max_down: a.download_kbps ?? -1, qos_rate_max_up: a.upload_kbps ?? -1 });
      return summarizeUsergroup(await restSave(client, site(a.site), "usergroup", a.id, body));
    },
  });

  defineTool(ctx, "unifi_delete_bandwidth_profile", {
    toolset: "networks",
    title: "Delete bandwidth profile",
    description: "Delete a bandwidth profile (user group). The default profile cannot be deleted.",
    delete: true,
    input: { ...siteArg, id: idArg("Bandwidth profile") },
    handler: async (a) => {
      const s = site(a.site);
      const group = await restGet(client, s, "usergroup", a.id);
      if (group.attr_no_delete) throw new Error(`"${group.name}" is the default bandwidth profile and cannot be deleted`);
      await restDelete(client, s, "usergroup", a.id);
      return { deleted: a.id, name: group.name };
    },
  });

  // ---------------------------------------------------------------- dynamic DNS

  defineTool(ctx, "unifi_list_dynamic_dns", {
    toolset: "networks",
    title: "List dynamic DNS",
    description: "List dynamic DNS configurations (passwords redacted unless include_secrets).",
    input: { ...siteArg, include_secrets: secretsArg },
    handler: async (a) => {
      const entries = await restList(client, site(a.site), "dynamicdns");
      const shown = a.include_secrets ? entries : redactSecrets(entries);
      return shown.map((d) =>
        compact({
          id: d._id,
          service: d.service,
          hostname: d.host_name,
          login: d.login,
          password: d.x_password,
          server: d.server,
          interface: d.interface,
          options: d.options?.length ? d.options : undefined,
        }),
      );
    },
  });

  defineTool(ctx, "unifi_save_dynamic_dns", {
    toolset: "networks",
    title: "Create/update dynamic DNS",
    description: "Create (no id) or update (id) a dynamic DNS configuration on the gateway.",
    write: true,
    input: {
      ...siteArg,
      id: idArg("Dynamic DNS entry").optional().describe("Entry ID to update; omit to create"),
      service: z
        .string()
        .optional()
        .describe("Provider, e.g. dyndns, noip, afraid, duckdns, dnspark, easydns, namecheap, zoneedit, cloudflare or custom (required on create)"),
      hostname: z.string().optional().describe("Hostname to update (required on create)"),
      login: z.string().optional().describe("Username / login"),
      password: z.string().optional().describe("Password or API token"),
      server: z.string().optional().describe("Update server (custom service)"),
      interface: z.string().optional().describe("WAN interface: wan (default) or wan2"),
      extra: extraArg,
    },
    handler: async (a) => {
      if (!a.id && (!a.service || !a.hostname)) throw new Error("service and hostname are required when creating a dynamic DNS entry");
      const body: Obj = defined({
        service: a.service,
        host_name: a.hostname,
        login: a.login,
        x_password: a.password,
        server: a.server,
        interface: a.interface,
      });
      if (!a.id) body.interface ??= "wan";
      Object.assign(body, a.extra);
      const saved = await restSave(client, site(a.site), "dynamicdns", a.id, body);
      return compact({ id: saved._id ?? a.id, service: saved.service, hostname: saved.host_name, interface: saved.interface });
    },
  });
}
