package unifi

// Raw UniFi objects are large and noisy. These helpers project them down to
// the fields that are useful to a model; tools expose a `raw` flag for the rest.

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

// Obj is a JSON object.
type Obj = ojson.Obj

// U is undefined, for leaving a field out.
var U = ojson.Undefined

var deviceStates = map[float64]string{
	0:  "disconnected",
	1:  "connected",
	2:  "pending_adoption",
	4:  "upgrading",
	5:  "provisioning",
	6:  "heartbeat_missed",
	7:  "adopting",
	9:  "adoption_error",
	10: "adoption_failed",
	11: "isolated",
}

var deviceTypes = map[string]string{
	"uap": "access_point",
	"usw": "switch",
	"ugw": "gateway",
	"udm": "gateway_console",
	"uxg": "gateway",
	"ubb": "building_bridge",
	"uck": "cloud_key",
	"uph": "phone",
	"usp": "smart_power",
}

// Compact builds an object from key/value pairs, dropping undefined, null and
// empty-string values so summaries stay small.
func Compact(kv ...any) *Obj {
	o := ojson.New()
	for i := 0; i+1 < len(kv); i += 2 {
		v := kv[i+1]
		if ojson.IsNullish(v) || v == "" {
			continue
		}
		o.Set(kv[i].(string), v)
	}
	return o
}

// CompactObj drops undefined, null and empty-string values from an object.
func CompactObj(src *Obj) *Obj {
	o := ojson.New()
	for _, k := range src.Keys() {
		v := src.Get(k)
		if ojson.IsNullish(v) || v == "" {
			continue
		}
		o.Set(k, v)
	}
	return o
}

// Defined builds an object from key/value pairs, dropping only undefined values
// (unlike Compact, "" and false and null are kept).
func Defined(kv ...any) *Obj {
	return ojson.New(kv...)
}

var nonHex = regexp.MustCompile(`[^0-9a-f]`)

// NormalizeMac turns aa-bb-cc-dd-ee-ff, AABBCCDDEEFF and so on into aa:bb:cc:dd:ee:ff.
func NormalizeMac(mac string) (string, error) {
	hex := nonHex.ReplaceAllString(strings.ToLower(mac), "")
	if len(hex) != 12 {
		return "", fmt.Errorf("Invalid MAC address: %s", mac)
	}
	pairs := make([]string, 6)
	for i := range pairs {
		pairs[i] = hex[i*2 : i*2+2]
	}
	return strings.Join(pairs, ":"), nil
}

// IsoTime converts epoch seconds (or milliseconds) to an ISO timestamp, or Undefined.
func IsoTime(epoch any) any {
	f, ok := ojson.Num(epoch)
	if !ok || !(f > 0) {
		return U
	}
	// Some endpoints use milliseconds.
	ms := f * 1000
	if f > 1e12 {
		ms = f
	}
	return isoString(ms)
}

// isoString is new Date(ms).toISOString().
func isoString(ms float64) any {
	if math.IsInf(ms, 0) || math.IsNaN(ms) || math.Abs(ms) > 8.64e15 {
		return U
	}
	ms = math.Trunc(ms)
	t := time.UnixMilli(int64(ms)).UTC()
	year := t.Year()
	rest := t.Format("-01-02T15:04:05.000Z")
	switch {
	case year >= 0 && year <= 9999:
		return fmt.Sprintf("%04d%s", year, rest)
	case year < 0:
		return fmt.Sprintf("-%06d%s", -year, rest)
	}
	return fmt.Sprintf("+%06d%s", year, rest)
}

// Duration formats seconds as "1d 2h 3m", "2h 3m" or "3m", or Undefined.
func Duration(seconds any) any {
	s, ok := ojson.Num(seconds)
	if !ok || s < 0 {
		return U
	}
	d := math.Floor(s / 86400)
	h := math.Floor(math.Mod(s, 86400) / 3600)
	m := math.Floor(math.Mod(s, 3600) / 60)
	n := ojson.FormatNumber
	switch {
	case ojson.Truthy(d):
		return fmt.Sprintf("%sd %sh %sm", n(d), n(h), n(m))
	case ojson.Truthy(h):
		return fmt.Sprintf("%sh %sm", n(h), n(m))
	}
	return fmt.Sprintf("%sm", n(m))
}

func mbpsFromKbps(v any) any {
	f, ok := ojson.Num(v)
	if !ok {
		return U
	}
	return ojson.Round(f/1000*10) / 10
}

// num is Number(v) for a value that is known to be present.
func num(v any) float64 { return ojson.ToNumber(v) }

// add is (a ?? 0) + (b ?? 0) || undefined.
func addOrUndefined(a, b any) any {
	sum := ojson.ToNumber(ojson.Coalesce(a, 0.0)) + ojson.ToNumber(ojson.Coalesce(b, 0.0))
	return ojson.OrUndefined(sum)
}

// SummarizeDevice projects a stat/device object.
func SummarizeDevice(d *Obj) *Obj {
	sys := ojson.Coalesce(d.Get("system-stats"), ojson.New())
	g := d.Get
	var state any = g("state")
	if f, ok := ojson.Num(state); ok {
		if s, ok := deviceStates[f]; ok {
			state = s
		}
	}
	var typ any = g("type")
	if s, ok := typ.(string); ok {
		if t, ok := deviceTypes[s]; ok {
			typ = t
		}
	}
	cpu, mem := ojson.Get(sys, "cpu"), ojson.Get(sys, "mem")
	var uplink any = U
	if ojson.Truthy(g("uplink")) {
		up := g("uplink")
		uplink = Compact(
			"type", ojson.Get(up, "type"),
			"device_mac", ojson.Get(up, "uplink_mac"),
			"remote_port", ojson.Get(up, "uplink_remote_port"),
			"speed_mbps", ojson.Get(up, "speed"),
		)
	}
	return Compact(
		"id", g("_id"),
		"name", ojson.Coalesce(g("name"), g("hostname")),
		"mac", g("mac"),
		"ip", g("ip"),
		"model", ojson.Coalesce(g("model_name"), g("model")),
		"model_code", g("model"),
		"type", typ,
		"state", state,
		"adopted", g("adopted"),
		"firmware", g("version"),
		"upgradable", ojson.OrUndefined(g("upgradable")),
		"upgrade_to_firmware", ojson.If(ojson.Truthy(g("upgradable")), g("upgrade_to_firmware"), U),
		"uptime", Duration(g("uptime")),
		"clients", g("num_sta"),
		"cpu_pct", ojson.If(!ojson.IsUndefined(cpu), num(cpu), U),
		"mem_pct", ojson.If(!ojson.IsUndefined(mem), num(mem), U),
		"temperature_c", g("general_temperature"),
		"uplink", uplink,
		"satisfaction", g("satisfaction"),
		"last_seen", IsoTime(g("last_seen")),
	)
}

// SummarizePort projects a port_table entry.
func SummarizePort(p *Obj) *Obj {
	g := p.Get
	up := ojson.Truthy(g("up"))
	return Compact(
		"idx", g("port_idx"),
		"name", g("name"),
		"up", g("up"),
		"enabled", g("enable"),
		"speed_mbps", ojson.If(up, g("speed"), U),
		"full_duplex", ojson.If(up, g("full_duplex"), U),
		"poe_mode", g("poe_mode"),
		"poe_power_w", ojson.If(ojson.Truthy(g("poe_power")), num(g("poe_power")), U),
		"is_uplink", ojson.OrUndefined(g("is_uplink")),
		"portconf_id", g("portconf_id"),
		"native_network_id", g("native_networkconf_id"),
		"media", g("media"),
		"rx_bytes", g("rx_bytes"),
		"tx_bytes", g("tx_bytes"),
		"errors", addOrUndefined(g("rx_errors"), g("tx_errors")),
	)
}

// SummarizeRadio projects a radio_table_stats entry.
func SummarizeRadio(r *Obj) *Obj {
	g := r.Get
	return Compact(
		"radio", g("radio"),
		"name", g("name"),
		"channel", g("channel"),
		"tx_power", g("tx_power"),
		"clients", ojson.Coalesce(g("num_sta"), g("user_num_sta")),
		"channel_utilization_pct", g("cu_total"),
		"satisfaction", g("satisfaction"),
	)
}

// SummarizeClient projects a stat/sta or rest/user object.
func SummarizeClient(c *Obj) *Obj {
	g := c.Get
	wired := ojson.Truthy(g("is_wired"))
	var connection any = U
	if !ojson.IsUndefined(g("is_wired")) {
		connection = ojson.If(wired, "wired", "wireless")
	}
	return Compact(
		"id", ojson.Coalesce(g("_id"), g("user_id")),
		"name", ojson.Coalesce(g("name"), g("hostname")),
		"hostname", g("hostname"),
		"mac", g("mac"),
		"ip", ojson.Coalesce(g("ip"), g("last_ip")),
		"fixed_ip", ojson.If(ojson.Truthy(g("use_fixedip")), g("fixed_ip"), U),
		"network", ojson.Coalesce(g("network"), g("last_connection_network_name")),
		"network_id", g("network_id"),
		"vlan", g("vlan"),
		"vendor", g("oui"),
		"connection", connection,
		"ssid", ojson.If(wired, U, g("essid")),
		"ap_mac", ojson.If(wired, U, g("ap_mac")),
		"locked_to_ap", ojson.If(ojson.Truthy(g("fixed_ap_enabled")), g("fixed_ap_mac"), U),
		"switch_mac", ojson.If(wired, g("sw_mac"), U),
		"switch_port", ojson.If(wired, g("sw_port"), U),
		"radio", ojson.Coalesce(g("radio_proto"), g("radio")),
		"channel", g("channel"),
		"signal_dbm", ojson.If(wired, U, g("signal")),
		"satisfaction", g("satisfaction"),
		"tx_rate_mbps", mbpsFromKbps(g("tx_rate")),
		"rx_rate_mbps", mbpsFromKbps(g("rx_rate")),
		"rx_bytes", g("rx_bytes"),
		"tx_bytes", g("tx_bytes"),
		"uptime", Duration(g("uptime")),
		"guest", ojson.OrUndefined(g("is_guest")),
		"authorized", ojson.If(ojson.Truthy(g("is_guest")), g("authorized"), U),
		"blocked", ojson.OrUndefined(g("blocked")),
		"note", g("note"),
		"first_seen", IsoTime(g("first_seen")),
		"last_seen", IsoTime(g("last_seen")),
	)
}

// SummarizeNetwork projects a networkconf object.
func SummarizeNetwork(n *Obj) *Obj {
	g := n.Get
	wan := ojson.StrictEq(g("purpose"), "wan")
	return Compact(
		"id", g("_id"),
		"name", g("name"),
		"purpose", g("purpose"),
		"enabled", g("enabled"),
		"vlan", ojson.If(ojson.Truthy(g("vlan_enabled")) || ojson.Truthy(g("vlan")), g("vlan"), U),
		"subnet", g("ip_subnet"),
		"dhcp_enabled", g("dhcpd_enabled"),
		"dhcp_range", ojson.If(ojson.Truthy(g("dhcpd_enabled")), ojson.String(g("dhcpd_start"))+"-"+ojson.String(g("dhcpd_stop")), U),
		"domain_name", g("domain_name"),
		"igmp_snooping", g("igmp_snooping"),
		"network_group", g("networkgroup"),
		"wan_type", ojson.If(wan, g("wan_type"), U),
		"wan_ip", ojson.If(wan, g("wan_ip"), U),
		"vpn_type", g("vpn_type"),
		"zone_id", g("firewall_zone_id"),
	)
}

// SummarizeWlan projects a wlanconf object.
func SummarizeWlan(w *Obj, includeSecrets bool) *Obj {
	g := w.Get
	return Compact(
		"id", g("_id"),
		"name", g("name"),
		"enabled", g("enabled"),
		"security", g("security"),
		"wpa_mode", g("wpa_mode"),
		"wpa3", ojson.OrUndefined(g("wpa3_support")),
		"passphrase", ojson.If(includeSecrets, g("x_passphrase"), U),
		"network_id", g("networkconf_id"),
		"hidden", ojson.OrUndefined(g("hide_ssid")),
		"guest", ojson.OrUndefined(g("is_guest")),
		"bands", ojson.Coalesce(g("wlan_bands"), g("wlan_band")),
		"ap_group_ids", g("ap_group_ids"),
		"client_isolation", ojson.OrUndefined(g("l2_isolation")),
		"mac_filter", ojson.If(ojson.Truthy(g("mac_filter_enabled")), g("mac_filter_policy"), U),
		"schedule_enabled", ojson.OrUndefined(g("schedule_enabled")),
	)
}

// secretField matches field names that hold secrets even without the
// controller's x_ prefix: SNMP communities, RADIUS/PPPoE/DDNS passwords,
// WireGuard private keys, OpenVPN configuration files, API tokens and so on.
// Objects nested under other names (e.g. private_preshared_keys[].password)
// are caught by recursion.
var secretField = regexp.MustCompile(`(?i)password|passphrase|secret|token|private_?key|_key$|configuration_file|config_file|^psk$|^community$`)

// notSecret lists names that match secretField but are identifiers, not secrets.
var notSecret = map[string]bool{"zone_key": true}

func isSecretField(name string) bool {
	return strings.HasPrefix(name, "x_") || (secretField.MatchString(name) && !notSecret[name])
}

// RedactSecrets strips secret fields (x_passphrase, x_password, community, private_key, ...) from arbitrary values.
func RedactSecrets(v any) any {
	if arr, ok := ojson.Arr(v); ok {
		return ojson.Map(arr, RedactSecrets)
	}
	o, ok := ojson.AsObj(v)
	if !ok {
		return v
	}
	out := ojson.New()
	for _, k := range o.Keys() {
		val := o.Get(k)
		secret := isSecretField(k)
		if s, ok := val.(string); secret && ok && s != "" {
			out.Set(k, "[redacted]")
		} else if arr, ok := ojson.Arr(val); secret && ok && len(arr) > 0 && allStrings(arr) {
			out.Set(k, ojson.Map(arr, func(any) any { return "[redacted]" }))
		} else {
			out.Set(k, RedactSecrets(val))
		}
	}
	return out
}

func allStrings(arr []any) bool {
	for _, x := range arr {
		if _, ok := x.(string); !ok {
			return false
		}
	}
	return true
}
