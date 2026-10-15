/**
 * Raw UniFi objects are large and noisy. These helpers project them down to the
 * fields that are useful to a model; tools expose a `raw` flag for the rest.
 */

type Obj = Record<string, any>;

const DEVICE_STATES: Record<number, string> = {
  0: "disconnected",
  1: "connected",
  2: "pending_adoption",
  4: "upgrading",
  5: "provisioning",
  6: "heartbeat_missed",
  7: "adopting",
  9: "adoption_error",
  10: "adoption_failed",
  11: "isolated",
};

const DEVICE_TYPES: Record<string, string> = {
  uap: "access_point",
  usw: "switch",
  ugw: "gateway",
  udm: "gateway_console",
  uxg: "gateway",
  ubb: "building_bridge",
  uck: "cloud_key",
  uph: "phone",
  usp: "smart_power",
};

/** Drop undefined/null/empty-string values so summaries stay small. */
export function compact<T extends Obj>(obj: T): Partial<T> {
  const out: Obj = {};
  for (const [k, v] of Object.entries(obj)) {
    if (v === undefined || v === null || v === "") continue;
    out[k] = v;
  }
  return out as Partial<T>;
}

export function normalizeMac(mac: string): string {
  const hex = mac.toLowerCase().replace(/[^0-9a-f]/g, "");
  if (hex.length !== 12) throw new Error(`Invalid MAC address: ${mac}`);
  return hex.match(/.{2}/g)!.join(":");
}

export function isoTime(epochSeconds: unknown): string | undefined {
  if (typeof epochSeconds !== "number" || epochSeconds <= 0) return undefined;
  // Some endpoints use milliseconds.
  const ms = epochSeconds > 1e12 ? epochSeconds : epochSeconds * 1000;
  return new Date(ms).toISOString();
}

export function duration(seconds: unknown): string | undefined {
  if (typeof seconds !== "number" || seconds < 0) return undefined;
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return d ? `${d}d ${h}h ${m}m` : h ? `${h}h ${m}m` : `${m}m`;
}

function mbps(bitsPerSecondOrKbps: unknown, unit: "kbps" | "bps"): number | undefined {
  if (typeof bitsPerSecondOrKbps !== "number") return undefined;
  const v = unit === "kbps" ? bitsPerSecondOrKbps / 1000 : bitsPerSecondOrKbps / 1e6;
  return Math.round(v * 10) / 10;
}

export function summarizeDevice(d: Obj): Obj {
  const sys = d["system-stats"] ?? {};
  return compact({
    id: d._id,
    name: d.name ?? d.hostname,
    mac: d.mac,
    ip: d.ip,
    model: d.model_name ?? d.model,
    model_code: d.model,
    type: DEVICE_TYPES[d.type] ?? d.type,
    state: DEVICE_STATES[d.state] ?? d.state,
    adopted: d.adopted,
    firmware: d.version,
    upgradable: d.upgradable || undefined,
    upgrade_to_firmware: d.upgradable ? d.upgrade_to_firmware : undefined,
    uptime: duration(d.uptime),
    clients: d.num_sta,
    cpu_pct: sys.cpu !== undefined ? Number(sys.cpu) : undefined,
    mem_pct: sys.mem !== undefined ? Number(sys.mem) : undefined,
    temperature_c: d.general_temperature,
    uplink: d.uplink
      ? compact({
          type: d.uplink.type,
          device_mac: d.uplink.uplink_mac,
          remote_port: d.uplink.uplink_remote_port,
          speed_mbps: d.uplink.speed,
        })
      : undefined,
    satisfaction: d.satisfaction,
    last_seen: isoTime(d.last_seen),
  });
}

export function summarizePort(p: Obj): Obj {
  return compact({
    idx: p.port_idx,
    name: p.name,
    up: p.up,
    enabled: p.enable,
    speed_mbps: p.up ? p.speed : undefined,
    full_duplex: p.up ? p.full_duplex : undefined,
    poe_mode: p.poe_mode,
    poe_power_w: p.poe_power ? Number(p.poe_power) : undefined,
    is_uplink: p.is_uplink || undefined,
    portconf_id: p.portconf_id,
    native_network_id: p.native_networkconf_id,
    media: p.media,
    rx_bytes: p.rx_bytes,
    tx_bytes: p.tx_bytes,
    errors: (p.rx_errors ?? 0) + (p.tx_errors ?? 0) || undefined,
  });
}

export function summarizeRadio(r: Obj): Obj {
  return compact({
    radio: r.radio,
    name: r.name,
    channel: r.channel,
    tx_power: r.tx_power,
    clients: r.num_sta ?? r.user_num_sta,
    channel_utilization_pct: r.cu_total,
    satisfaction: r.satisfaction,
  });
}

export function summarizeClient(c: Obj): Obj {
  return compact({
    id: c._id ?? c.user_id,
    name: c.name ?? c.hostname,
    hostname: c.hostname,
    mac: c.mac,
    ip: c.ip ?? c.last_ip,
    fixed_ip: c.use_fixedip ? c.fixed_ip : undefined,
    network: c.network ?? c.last_connection_network_name,
    network_id: c.network_id,
    vlan: c.vlan,
    vendor: c.oui,
    connection: c.is_wired === undefined ? undefined : c.is_wired ? "wired" : "wireless",
    ssid: c.is_wired ? undefined : c.essid,
    ap_mac: c.is_wired ? undefined : c.ap_mac,
    switch_mac: c.is_wired ? c.sw_mac : undefined,
    switch_port: c.is_wired ? c.sw_port : undefined,
    radio: c.radio_proto ?? c.radio,
    channel: c.channel,
    signal_dbm: c.is_wired ? undefined : c.signal,
    satisfaction: c.satisfaction,
    tx_rate_mbps: mbps(c.tx_rate, "kbps"),
    rx_rate_mbps: mbps(c.rx_rate, "kbps"),
    rx_bytes: c.rx_bytes,
    tx_bytes: c.tx_bytes,
    uptime: duration(c.uptime),
    guest: c.is_guest || undefined,
    authorized: c.is_guest ? c.authorized : undefined,
    blocked: c.blocked || undefined,
    note: c.note,
    first_seen: isoTime(c.first_seen),
    last_seen: isoTime(c.last_seen),
  });
}

export function summarizeNetwork(n: Obj): Obj {
  return compact({
    id: n._id,
    name: n.name,
    purpose: n.purpose,
    enabled: n.enabled,
    vlan: n.vlan_enabled || n.vlan ? n.vlan : undefined,
    subnet: n.ip_subnet,
    dhcp_enabled: n.dhcpd_enabled,
    dhcp_range: n.dhcpd_enabled ? `${n.dhcpd_start}-${n.dhcpd_stop}` : undefined,
    domain_name: n.domain_name,
    igmp_snooping: n.igmp_snooping,
    network_group: n.networkgroup,
    wan_type: n.purpose === "wan" ? n.wan_type : undefined,
    wan_ip: n.purpose === "wan" ? n.wan_ip : undefined,
    vpn_type: n.vpn_type,
    zone_id: n.firewall_zone_id,
  });
}

export function summarizeWlan(w: Obj, includeSecrets: boolean): Obj {
  return compact({
    id: w._id,
    name: w.name,
    enabled: w.enabled,
    security: w.security,
    wpa_mode: w.wpa_mode,
    wpa3: w.wpa3_support || undefined,
    passphrase: includeSecrets ? w.x_passphrase : undefined,
    network_id: w.networkconf_id,
    hidden: w.hide_ssid || undefined,
    guest: w.is_guest || undefined,
    bands: w.wlan_bands ?? w.wlan_band,
    ap_group_ids: w.ap_group_ids,
    client_isolation: w.l2_isolation || undefined,
    mac_filter: w.mac_filter_enabled ? w.mac_filter_policy : undefined,
    schedule_enabled: w.schedule_enabled || undefined,
  });
}

/**
 * Field names that hold secrets even without the controller's x_ prefix: SNMP
 * communities, RADIUS/PPPoE/DDNS passwords, WireGuard private keys, OpenVPN
 * configuration files, API tokens and so on. Objects nested under other names
 * (e.g. private_preshared_keys[].password) are caught by recursion.
 */
const SECRET_FIELD = /password|passphrase|secret|token|private_?key|_key$|configuration_file|config_file|^psk$|^community$/i;
/** Names that match SECRET_FIELD but are identifiers, not secrets. */
const NOT_SECRET = new Set(["zone_key"]);

function isSecretField(name: string): boolean {
  return name.startsWith("x_") || (SECRET_FIELD.test(name) && !NOT_SECRET.has(name));
}

/** Strip secret fields (x_passphrase, x_password, community, private_key, ...) from arbitrary objects. */
export function redactSecrets<T>(value: T): T {
  if (Array.isArray(value)) return value.map(redactSecrets) as T;
  if (value && typeof value === "object") {
    const out: Obj = {};
    for (const [k, v] of Object.entries(value)) {
      const secret = isSecretField(k);
      if (secret && typeof v === "string" && v) out[k] = "[redacted]";
      else if (secret && Array.isArray(v) && v.length && v.every((x) => typeof x === "string")) out[k] = v.map(() => "[redacted]");
      else out[k] = redactSecrets(v);
    }
    return out as T;
  }
  return value;
}
