import { z } from "zod";
import { compact, redactSecrets } from "../unifi/format.js";
import { restDelete, restGet, restList, restSave } from "./rest.js";
import { defined, defineTool, extraArg, idArg, siteArg, type ToolContext } from "./util.js";

const rawArg = z.boolean().optional().describe("Return the full unprocessed objects from the controller (large)");

const secretsArg = z
  .boolean()
  .optional()
  .describe("Include secret fields such as pre-shared keys, private keys and passwords (redacted by default)");

type Obj = Record<string, any>;

const VPN_PURPOSES = ["remote-user-vpn", "site-vpn", "vpn-client"];

/** Friendly VPN kind -> networkconf purpose / vpn_type. */
const VPN_TYPES = {
  "wireguard-server": { purpose: "remote-user-vpn", vpn_type: "wireguard-server" },
  "openvpn-server": { purpose: "remote-user-vpn", vpn_type: "openvpn-server" },
  "l2tp-server": { purpose: "remote-user-vpn", vpn_type: "l2tp-server" },
  "wireguard-client": { purpose: "vpn-client", vpn_type: "wireguard-client" },
  "openvpn-client": { purpose: "vpn-client", vpn_type: "openvpn-client" },
  "ipsec-site-to-site": { purpose: "site-vpn", vpn_type: "ipsec-vpn" },
  "openvpn-site-to-site": { purpose: "site-vpn", vpn_type: "openvpn-vpn" },
} as const;
type VpnKind = keyof typeof VPN_TYPES;

function summarizeVpn(n: Obj): Obj {
  const kind =
    (Object.entries(VPN_TYPES).find(([, t]) => t.purpose === n.purpose && t.vpn_type === n.vpn_type)?.[0] as VpnKind | undefined) ??
    `${n.purpose}/${n.vpn_type ?? "?"}`;
  return compact({
    id: n._id,
    name: n.name,
    kind,
    enabled: n.enabled,
    subnet: n.ip_subnet,
    port: n.local_port,
    wan_interface: n.l2tp_interface ?? n.wireguard_interface ?? n.openvpn_interface ?? n.ipsec_interface,
    peer_ip: n.ipsec_peer_ip ?? n.openvpn_remote_host,
    local_ip: n.ipsec_local_ip,
    remote_subnets: n.remote_vpn_subnets?.length ? n.remote_vpn_subnets : undefined,
    key_exchange: n.ipsec_key_exchange,
    radius_profile_id: n.radiusprofile_id,
    dns: [n.dhcpd_dns_1, n.dhcpd_dns_2].filter(Boolean).length ? [n.dhcpd_dns_1, n.dhcpd_dns_2].filter(Boolean) : undefined,
  });
}

function summarizeRadiusProfile(p: Obj, includeSecrets: boolean): Obj {
  const servers = (list: Obj[] | undefined) =>
    list?.length ? list.map((s) => compact({ ip: s.ip, port: s.port, secret: includeSecrets ? s.x_secret : undefined })) : undefined;
  return compact({
    id: p._id,
    name: p.name,
    auth_servers: servers(p.auth_servers),
    accounting_enabled: p.accounting_enabled,
    acct_servers: servers(p.acct_servers),
    vlan_enabled: p.vlan_enabled || undefined,
    vlan_wlan_mode: p.vlan_enabled ? p.vlan_wlan_mode : undefined,
    built_in: p.attr_no_delete || undefined,
  });
}

function summarizeRadiusAccount(a: Obj, includeSecrets: boolean): Obj {
  return compact({
    id: a._id,
    name: a.name,
    password: includeSecrets ? a.x_password : undefined,
    vlan: a.vlan,
    tunnel_type: a.tunnel_type,
    tunnel_medium_type: a.tunnel_medium_type,
    network_id: a.networkconf_id,
  });
}

const serverArg = z.object({
  ip: z.string().describe("RADIUS server IP"),
  port: z.number().int().min(1).max(65535).optional().describe("Port (1812 auth / 1813 accounting by default)"),
  secret: z.string().describe("Shared secret"),
});

export function registerVpnTools(ctx: ToolContext): void {
  const { client } = ctx;
  const site = (s?: string) => s ?? client.defaultSite;

  // ---------------------------------------------------------------- VPN networks

  defineTool(ctx, "unifi_list_vpns", {
    toolset: "vpn",
    title: "List VPNs",
    description:
      "List VPN servers (WireGuard, OpenVPN, L2TP), VPN clients and site-to-site VPNs configured on the gateway. Use raw: true to see every field.",
    input: { ...siteArg, raw: rawArg, include_secrets: secretsArg },
    handler: async (a) => {
      const vpns = (await restList(client, site(a.site), "networkconf")).filter((n) => VPN_PURPOSES.includes(n.purpose));
      if (a.raw) return a.include_secrets ? vpns : redactSecrets(vpns);
      return vpns.map(summarizeVpn);
    },
  });

  defineTool(ctx, "unifi_save_vpn", {
    toolset: "vpn",
    title: "Create/update VPN",
    description:
      "Create (no id; kind required) or update (id; only fields passed change) a VPN server, VPN client or site-to-site VPN. " +
      "Typed fields cover the common settings; anything else (IPsec proposals, OpenVPN/WireGuard client configuration, peer keys) goes in `extra` using the controller's field names — " +
      "inspect an existing VPN with unifi_list_vpns raw: true to see them, since they vary between Network versions. " +
      "WireGuard server peers (users) and imported client config files are not covered by this tool; use unifi_api_request.",
    write: true,
    destructive: true,
    input: {
      ...siteArg,
      id: idArg("VPN").optional().describe("VPN ID to update; omit to create"),
      kind: z.enum(Object.keys(VPN_TYPES) as [VpnKind, ...VpnKind[]]).optional().describe("VPN type (required on create)"),
      name: z.string().min(1).optional().describe("Name (required on create)"),
      enabled: z.boolean().optional(),
      subnet: z.string().optional().describe("Tunnel/client address pool as gateway/prefix, e.g. 192.168.3.1/24 (VPN servers)"),
      port: z.number().int().min(1).max(65535).optional().describe("Listen port (WireGuard default 51820, OpenVPN 1194)"),
      wan_interface: z.string().optional().describe("WAN to listen on: wan (default) or wan2 (L2TP/OpenVPN/WireGuard servers)"),
      pre_shared_key: z.string().optional().describe("IPsec pre-shared key (L2TP server, IPsec site-to-site)"),
      peer_ip: z.string().optional().describe("Remote peer public IP or hostname (site-to-site)"),
      local_ip: z.string().optional().describe("Local WAN IP used for the tunnel (IPsec site-to-site)"),
      remote_subnets: z.array(z.string()).optional().describe("Remote networks reachable over the tunnel, CIDR (site-to-site)"),
      radius_profile_id: z.string().optional().describe("RADIUS profile authenticating L2TP users (see unifi_list_radius_profiles)"),
      dns: z.array(z.string()).max(2).optional().describe("DNS servers given to VPN clients; empty array = automatic"),
      extra: extraArg,
    },
    handler: async (a) => {
      const s = site(a.site);
      let existing: Obj | undefined;
      if (a.id) {
        existing = await restGet(client, s, "networkconf", a.id);
        if (!VPN_PURPOSES.includes(existing.purpose)) throw new Error(`Network ${a.id} ("${existing.name}") is not a VPN`);
        if (a.kind) {
          const t = VPN_TYPES[a.kind];
          if (t.purpose !== existing.purpose || t.vpn_type !== existing.vpn_type) {
            throw new Error("A VPN's kind cannot be changed; delete it and create a new one");
          }
        }
      } else if (!a.kind || !a.name) {
        throw new Error("kind and name are required when creating a VPN");
      }
      const kind = (a.kind ?? summarizeVpn(existing!).kind) as string;

      const body: Obj = defined({
        name: a.name,
        enabled: a.enabled,
        ip_subnet: a.subnet,
        local_port: a.port,
        x_ipsec_pre_shared_key: a.pre_shared_key,
        ipsec_peer_ip: a.peer_ip,
        ipsec_local_ip: a.local_ip,
        remote_vpn_subnets: a.remote_subnets,
        radiusprofile_id: a.radius_profile_id,
      });
      if (a.wan_interface) {
        const field = kind.startsWith("l2tp")
          ? "l2tp_interface"
          : kind.startsWith("wireguard")
            ? "wireguard_interface"
            : kind.startsWith("openvpn")
              ? "openvpn_interface"
              : "ipsec_interface";
        body[field] = a.wan_interface;
      }
      if (a.dns) {
        body.dhcpd_dns_enabled = a.dns.length > 0;
        body.dhcpd_dns_1 = a.dns[0] ?? "";
        body.dhcpd_dns_2 = a.dns[1] ?? "";
      }

      if (!existing) {
        const t = VPN_TYPES[a.kind!];
        if (t.purpose === "remote-user-vpn" && !a.subnet) throw new Error("subnet is required when creating a VPN server");
        if (t.purpose === "site-vpn" && (!a.peer_ip || !a.remote_subnets?.length)) {
          throw new Error("peer_ip and remote_subnets are required when creating a site-to-site VPN");
        }
        if ((a.kind === "l2tp-server" || a.kind === "ipsec-site-to-site") && !a.pre_shared_key) {
          throw new Error("pre_shared_key is required for L2TP and IPsec VPNs");
        }
        if (a.kind === "l2tp-server" && !a.radius_profile_id) {
          const profiles = await restList(client, s, "radiusprofile").catch(() => [] as Obj[]);
          const def = profiles.find((p) => p.attr_no_delete) ?? profiles[0];
          if (def) body.radiusprofile_id = def._id;
        }
        Object.assign(body, {
          purpose: t.purpose,
          vpn_type: t.vpn_type,
          enabled: a.enabled ?? true,
          ...(a.kind === "wireguard-server" ? { local_port: a.port ?? 51820 } : {}),
          ...(a.kind === "openvpn-server" ? { local_port: a.port ?? 1194 } : {}),
          ...(a.kind === "ipsec-site-to-site" ? { ipsec_key_exchange: "ikev2" } : {}),
          ...body,
        });
      }
      Object.assign(body, a.extra);

      return summarizeVpn(await restSave(client, s, "networkconf", a.id, body));
    },
  });

  defineTool(ctx, "unifi_delete_vpn", {
    toolset: "vpn",
    title: "Delete VPN",
    description: "Delete a VPN server, VPN client or site-to-site VPN. Connected VPN users/tunnels drop.",
    delete: true,
    input: { ...siteArg, id: idArg("VPN") },
    handler: async (a) => {
      const s = site(a.site);
      const net = await restGet(client, s, "networkconf", a.id);
      if (!VPN_PURPOSES.includes(net.purpose)) throw new Error(`Network ${a.id} ("${net.name}") is not a VPN; use unifi_delete_network`);
      await restDelete(client, s, "networkconf", a.id);
      return { deleted: a.id, name: net.name };
    },
  });

  // ---------------------------------------------------------------- RADIUS profiles

  defineTool(ctx, "unifi_list_radius_profiles", {
    toolset: "vpn",
    title: "List RADIUS profiles",
    description: "List RADIUS profiles used by WPA-Enterprise WiFi, 802.1X switch ports and L2TP VPN (secrets redacted unless include_secrets).",
    input: { ...siteArg, include_secrets: secretsArg },
    handler: async (a) =>
      (await restList(client, site(a.site), "radiusprofile")).map((p) => summarizeRadiusProfile(p, Boolean(a.include_secrets))),
  });

  defineTool(ctx, "unifi_save_radius_profile", {
    toolset: "vpn",
    title: "Create/update RADIUS profile",
    description:
      "Create (no id) or update (id; only fields passed change) a RADIUS profile pointing at external RADIUS servers. Server lists replace the existing ones.",
    write: true,
    input: {
      ...siteArg,
      id: idArg("RADIUS profile").optional().describe("Profile ID to update; omit to create"),
      name: z.string().min(1).optional().describe("Name (required on create)"),
      auth_servers: z.array(serverArg).optional().describe("Authentication servers"),
      accounting_enabled: z.boolean().optional(),
      acct_servers: z.array(serverArg).optional().describe("Accounting servers"),
      vlan_enabled: z.boolean().optional().describe("Assign VLANs from RADIUS attributes"),
      extra: extraArg,
    },
    handler: async (a) => {
      if (!a.id && (!a.name || !a.auth_servers?.length)) throw new Error("name and auth_servers are required when creating a RADIUS profile");
      const servers = (list: z.infer<typeof serverArg>[] | undefined, port: number) =>
        list?.map((s) => ({ ip: s.ip, port: s.port ?? port, x_secret: s.secret }));
      const body: Obj = defined({
        name: a.name,
        auth_servers: servers(a.auth_servers, 1812),
        accounting_enabled: a.accounting_enabled,
        acct_servers: servers(a.acct_servers, 1813),
        vlan_enabled: a.vlan_enabled,
      });
      if (!a.id) Object.assign(body, { accounting_enabled: a.accounting_enabled ?? false, acct_servers: body.acct_servers ?? [], vlan_enabled: a.vlan_enabled ?? false });
      Object.assign(body, a.extra);
      return summarizeRadiusProfile(await restSave(client, site(a.site), "radiusprofile", a.id, body), false);
    },
  });

  defineTool(ctx, "unifi_delete_radius_profile", {
    toolset: "vpn",
    title: "Delete RADIUS profile",
    description: "Delete a RADIUS profile. The built-in default profile cannot be deleted.",
    delete: true,
    input: { ...siteArg, id: idArg("RADIUS profile") },
    handler: async (a) => {
      const s = site(a.site);
      const profile = await restGet(client, s, "radiusprofile", a.id);
      if (profile.attr_no_delete) throw new Error(`"${profile.name}" is the built-in RADIUS profile and cannot be deleted`);
      await restDelete(client, s, "radiusprofile", a.id);
      return { deleted: a.id, name: profile.name };
    },
  });

  // ---------------------------------------------------------------- RADIUS accounts

  defineTool(ctx, "unifi_list_radius_accounts", {
    toolset: "vpn",
    title: "List RADIUS accounts",
    description: "List users of the built-in RADIUS server (used for L2TP VPN and WPA-Enterprise). Passwords redacted unless include_secrets.",
    input: { ...siteArg, include_secrets: secretsArg },
    handler: async (a) =>
      (await restList(client, site(a.site), "account")).map((x) => summarizeRadiusAccount(x, Boolean(a.include_secrets))),
  });

  defineTool(ctx, "unifi_save_radius_account", {
    toolset: "vpn",
    title: "Create/update RADIUS account",
    description:
      "Create (no id) or update (id) a user of the built-in RADIUS server. For per-user VLAN assignment set vlan together with tunnel_type 13 (VLAN) and tunnel_medium_type 6 (802).",
    write: true,
    input: {
      ...siteArg,
      id: idArg("RADIUS account").optional().describe("Account ID to update; omit to create"),
      name: z.string().min(1).optional().describe("Username (required on create)"),
      password: z.string().min(1).optional().describe("Password (required on create)"),
      vlan: z.number().int().min(1).max(4095).optional().describe("VLAN ID to assign to this user"),
      tunnel_type: z.number().int().min(1).max(13).optional().describe("RADIUS Tunnel-Type (3 = L2TP, 13 = VLAN)"),
      tunnel_medium_type: z.number().int().min(1).max(15).optional().describe("RADIUS Tunnel-Medium-Type (1 = IPv4, 6 = 802)"),
      extra: extraArg,
    },
    handler: async (a) => {
      if (!a.id && (!a.name || !a.password)) throw new Error("name and password are required when creating a RADIUS account");
      if ((a.tunnel_type === undefined) !== (a.tunnel_medium_type === undefined)) {
        throw new Error("tunnel_type and tunnel_medium_type must be set together");
      }
      const body: Obj = defined({
        name: a.name,
        x_password: a.password,
        vlan: a.vlan,
        tunnel_type: a.tunnel_type,
        tunnel_medium_type: a.tunnel_medium_type,
      });
      Object.assign(body, a.extra);
      return summarizeRadiusAccount(await restSave(client, site(a.site), "account", a.id, body), false);
    },
  });

  defineTool(ctx, "unifi_delete_radius_account", {
    toolset: "vpn",
    title: "Delete RADIUS account",
    description: "Delete a user of the built-in RADIUS server.",
    delete: true,
    input: { ...siteArg, id: idArg("RADIUS account") },
    handler: async (a) => {
      const s = site(a.site);
      const account = await restGet(client, s, "account", a.id);
      await restDelete(client, s, "account", a.id);
      return { deleted: a.id, name: account.name };
    },
  });
}
