// Package config reads unifi-mcp's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

// Toolsets are groups of tools that can be switched on and off with UNIFI_TOOLSETS.
var Toolsets = []string{
	"overview",
	"devices",
	"clients",
	"switching",
	"wifi",
	"networks",
	"firewall",
	"routing",
	"security",
	"vpn",
	"hotspot",
	"monitoring",
	"admin",
	"raw",
}

// Controller types for UNIFI_CONTROLLER_TYPE.
const (
	ControllerAuto    = "auto"
	ControllerUnifiOS = "unifi-os"
	ControllerLegacy  = "legacy"
)

// Unifi holds the controller connection settings.
type Unifi struct {
	URL      string
	APIKey   string
	Username string
	Password string
	Site     string
	// VerifySSL turns normal certificate verification on.
	VerifySSL bool
	// CAFile is a PEM file with CA certificates to trust for the controller (UNIFI_CA_FILE).
	CAFile string
	// TLSFingerprints are the accepted SHA-256 fingerprints of the controller's certificate (UNIFI_TLS_FINGERPRINT), as AA:BB:...
	TLSFingerprints []string
	ControllerType  string
	TimeoutMs       int
}

// ToolModeRemoved is the error when UNIFI_TOOL_MODE, removed in 0.6.0 with
// lazy tool loading, is still set.
const ToolModeRemoved = "UNIFI_TOOL_MODE was removed in 0.6.0: every enabled tool is listed. Use UNIFI_TOOLSETS to keep the list small (see docs/configuration.md)."

// Tools controls which tools are registered.
type Tools struct {
	AllowWrites bool
	// AllowDeletes enables tools that delete configuration objects; requires AllowWrites as well.
	AllowDeletes bool
	// AllowSecrets offers the include_secrets argument and the tools that
	// reveal secrets by design, such as unifi_get_wifi_qr (UNIFI_ALLOW_SECRETS).
	// Without it, output is always redacted, whatever a client asks for.
	AllowSecrets bool
	Toolsets     map[string]bool
	// AuditLog is where calls of tools that change things are logged
	// (UNIFI_AUDIT_LOG): a file path, AuditLogStderr, or "" for no log.
	AuditLog string
}

// AuditLogStderr is the UNIFI_AUDIT_LOG value that writes the audit log to standard error.
const AuditLogStderr = "stderr"

// AllTools enables every tool, for documentation and tests.
func AllTools() Tools {
	t := Tools{AllowWrites: true, AllowDeletes: true, AllowSecrets: true, Toolsets: map[string]bool{}}
	for _, ts := range Toolsets {
		t.Toolsets[ts] = true
	}
	return t
}

// HTTP holds the Streamable HTTP transport settings.
type HTTP struct {
	Host      string
	Port      int
	AuthToken string
	// AllowUnauthenticated serves /mcp without a bearer token (MCP_ALLOW_UNAUTHENTICATED).
	AllowUnauthenticated bool
	AllowedHosts         []string
}

// Config is the whole configuration.
type Config struct {
	Unifi     Unifi
	Tools     Tools
	Transport string // "stdio" or "http"
	HTTP      HTTP
}

// Env looks up a variable; os.LookupEnv in production, a map in tests.
type Env func(string) (string, bool)

// MapEnv adapts a map for tests.
func MapEnv(m map[string]string) Env {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// placeholder matches a ${user_config.<key>} reference that an MCP Bundle host
// such as Claude Desktop passed through unchanged because the user left an
// optional setting empty. It means "not set", not a value.
var placeholder = regexp.MustCompile(`^\$\{user_config\.[^}]*\}$`)

// lookup returns a trimmed variable, or "" when it is unset, blank or an
// unsubstituted bundle placeholder.
func lookup(env Env, name string) string {
	v, _ := env(name)
	v = strings.TrimSpace(v)
	if placeholder.MatchString(v) {
		return ""
	}
	return v
}

// read returns a trimmed variable (empty when unset, blank or an unfilled
// bundle placeholder), honouring the <NAME>_FILE convention so secrets can be
// supplied as Docker secrets.
func read(env Env, name string) (string, error) {
	if file := lookup(env, name+"_FILE"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("Could not read %s_FILE (%s): %s", name, file, errorMessage(err))
		}
		return strings.TrimSpace(string(b)), nil
	}
	return lookup(env, name), nil
}

// errorMessage describes a file error the way Node does (ENOENT: no such file or directory, open '...').
func errorMessage(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		code := ""
		switch {
		case errors.Is(err, os.ErrNotExist):
			code = "ENOENT: no such file or directory"
		case errors.Is(err, os.ErrPermission):
			code = "EACCES: permission denied"
		}
		if code != "" {
			return fmt.Sprintf("%s, %s '%s'", code, pe.Op, pe.Path)
		}
	}
	return err.Error()
}

func boolValue(v string, fallback bool) bool {
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func positiveInt(name, v string, fallback int) (int, error) {
	if v == "" {
		return fallback, nil
	}
	n, ok := ojson.ParseInt(v)
	if !ok || n <= 0 || n > 1<<53 {
		return 0, fmt.Errorf(`%s must be a positive integer, got "%s"`, name, v)
	}
	return int(n), nil
}

// MinAuthTokenLength is the shortest MCP_AUTH_TOKEN accepted.
const MinAuthTokenLength = 16

// UnauthenticatedNeedsHosts is the error for MCP_ALLOW_UNAUTHENTICATED=true without MCP_ALLOWED_HOSTS.
// Without a token, the Host check is the only thing stopping a web page from reaching
// the server through DNS rebinding.
const UnauthenticatedNeedsHosts = "MCP_ALLOW_UNAUTHENTICATED=true also requires MCP_ALLOWED_HOSTS: without a token, a web page you visit could reach the server through DNS rebinding. List the host names clients use to reach it (e.g. MCP_ALLOWED_HOSTS=unifi-mcp.example.com)"

var trailingSlashes = regexp.MustCompile(`/+$`)

// Load builds the configuration from the environment.
func Load(env Env) (*Config, error) {
	var firstErr error
	get := func(name string) string {
		v, err := read(env, name)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return v
	}
	// A variable that can't be read is reported before anything that depends on it.
	check := func() error { return firstErr }
	or := func(v, fallback string) string {
		if v == "" {
			return fallback
		}
		return v
	}

	rawURL := get("UNIFI_URL")
	if err := check(); err != nil {
		return nil, err
	}
	if rawURL == "" {
		return nil, errors.New("UNIFI_URL is required (e.g. https://192.168.1.1)")
	}
	if u, err := url.Parse(rawURL); err == nil && u.User != nil {
		// Credentials in the URL would end up in logs and error messages.
		return nil, errors.New("UNIFI_URL must not contain a username or password; use UNIFI_API_KEY, or UNIFI_USERNAME and UNIFI_PASSWORD")
	}
	if !validURL(rawURL) {
		return nil, fmt.Errorf("UNIFI_URL is not a valid URL: %s", rawURL)
	}

	apiKey, username, password := get("UNIFI_API_KEY"), get("UNIFI_USERNAME"), get("UNIFI_PASSWORD")
	if err := check(); err != nil {
		return nil, err
	}
	if apiKey == "" && (username == "" || password == "") {
		return nil, errors.New("Set either UNIFI_API_KEY or both UNIFI_USERNAME and UNIFI_PASSWORD")
	}

	controllerType := or(get("UNIFI_CONTROLLER_TYPE"), ControllerAuto)
	if err := check(); err != nil {
		return nil, err
	}
	if controllerType != ControllerAuto && controllerType != ControllerUnifiOS && controllerType != ControllerLegacy {
		return nil, fmt.Errorf(`UNIFI_CONTROLLER_TYPE must be auto, unifi-os or legacy, got "%s"`, controllerType)
	}
	if apiKey != "" && controllerType == ControllerLegacy {
		return nil, errors.New("API keys are only supported by UniFi OS consoles; use UNIFI_USERNAME/UNIFI_PASSWORD for legacy controllers")
	}

	cfg := &Config{Transport: or(get("MCP_TRANSPORT"), "stdio")}
	if err := check(); err != nil {
		return nil, err
	}
	if cfg.Transport != "stdio" && cfg.Transport != "http" {
		return nil, fmt.Errorf(`MCP_TRANSPORT must be stdio or http, got "%s"`, cfg.Transport)
	}

	t := &cfg.Tools
	t.AllowWrites = boolValue(get("UNIFI_ALLOW_WRITES"), false)
	t.AllowDeletes = boolValue(get("UNIFI_ALLOW_DELETES"), false)
	t.AllowSecrets = boolValue(get("UNIFI_ALLOW_SECRETS"), false)
	if err := check(); err != nil {
		return nil, err
	}
	if t.AllowDeletes && !t.AllowWrites {
		return nil, errors.New("UNIFI_ALLOW_DELETES=true also requires UNIFI_ALLOW_WRITES=true")
	}

	// Blank, or the placeholder an older bundle passes for an empty field, counts as unset.
	if lookup(env, "UNIFI_TOOL_MODE") != "" {
		return nil, errors.New(ToolModeRemoved)
	}
	t.AuditLog = get("UNIFI_AUDIT_LOG")
	if err := check(); err != nil {
		return nil, err
	}
	if err := CheckAuditLog(t.AuditLog); err != nil {
		return nil, err
	}

	t.Toolsets = map[string]bool{}
	toolsets := or(get("UNIFI_TOOLSETS"), "all")
	if err := check(); err != nil {
		return nil, err
	}
	for _, name := range strings.Split(toolsets, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		switch {
		case name == "":
		case name == "all":
			for _, ts := range Toolsets {
				t.Toolsets[ts] = true
			}
		case contains(Toolsets, name):
			t.Toolsets[name] = true
		default:
			return nil, fmt.Errorf(`Unknown toolset "%s" in UNIFI_TOOLSETS; valid: all, %s`, name, strings.Join(Toolsets, ", "))
		}
	}

	h := &cfg.HTTP
	for _, host := range strings.Split(get("MCP_ALLOWED_HOSTS"), ",") {
		if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
			h.AllowedHosts = append(h.AllowedHosts, host)
		}
	}
	h.AuthToken = get("MCP_AUTH_TOKEN")
	h.AllowUnauthenticated = boolValue(get("MCP_ALLOW_UNAUTHENTICATED"), false)
	if err := check(); err != nil {
		return nil, err
	}
	// An HTTP server without a token would hand every enabled tool to anyone who can reach the port.
	if cfg.Transport == "http" && h.AuthToken == "" && !h.AllowUnauthenticated {
		return nil, errors.New("MCP_AUTH_TOKEN is required for the http transport (generate one with: openssl rand -hex 32); set MCP_ALLOW_UNAUTHENTICATED=true only behind something else that authenticates")
	}
	if cfg.Transport == "http" && h.AuthToken != "" && len(h.AuthToken) < MinAuthTokenLength {
		return nil, fmt.Errorf("MCP_AUTH_TOKEN must be at least %d characters (generate one with: openssl rand -hex 32)", MinAuthTokenLength)
	}
	if cfg.Transport == "http" && h.AuthToken == "" && len(h.AllowedHosts) == 0 {
		return nil, errors.New(UnauthenticatedNeedsHosts)
	}

	u := &cfg.Unifi
	u.CAFile = get("UNIFI_CA_FILE")
	if err := check(); err != nil {
		return nil, err
	}
	if u.CAFile != "" {
		if _, err := os.ReadFile(u.CAFile); err != nil {
			return nil, fmt.Errorf("Could not read UNIFI_CA_FILE (%s): %s", u.CAFile, errorMessage(err))
		}
	}
	// A custom CA only makes sense with verification on, so it switches it on by default.
	verifySSL := get("UNIFI_VERIFY_SSL")
	u.VerifySSL = boolValue(verifySSL, u.CAFile != "")
	if err := check(); err != nil {
		return nil, err
	}
	if u.CAFile != "" && !u.VerifySSL {
		return nil, errors.New("UNIFI_CA_FILE needs certificate verification; remove UNIFI_VERIFY_SSL=false")
	}
	for _, f := range strings.Split(get("UNIFI_TLS_FINGERPRINT"), ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		fp, err := NormalizeFingerprint(f)
		if err != nil {
			return nil, fmt.Errorf("UNIFI_TLS_FINGERPRINT: %s", err.Error())
		}
		u.TLSFingerprints = append(u.TLSFingerprints, fp)
	}
	// Consoles ship with self-signed certificates, so verification used to be off by
	// default, which let anyone on the path impersonate the controller and capture
	// its credentials. Now the choice has to be made explicitly.
	if verifySSL == "" && u.CAFile == "" && len(u.TLSFingerprints) == 0 && strings.HasPrefix(strings.ToLower(rawURL), "https:") {
		return nil, errors.New(TLSChoiceRequired)
	}

	u.URL = trailingSlashes.ReplaceAllString(rawURL, "")
	u.APIKey, u.Username, u.Password = apiKey, username, password
	u.Site = or(get("UNIFI_SITE"), "default")
	u.ControllerType = controllerType
	var err error
	if u.TimeoutMs, err = positiveInt("UNIFI_TIMEOUT_MS", get("UNIFI_TIMEOUT_MS"), 30000); err != nil {
		return nil, err
	}
	h.Host = or(get("MCP_HTTP_HOST"), "127.0.0.1")
	if h.Port, err = positiveInt("MCP_HTTP_PORT", get("MCP_HTTP_PORT"), 8080); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// AuditLogMode is the permission an audit log file is created with: only the
// server's user can read it, since it records what was changed and when.
const AuditLogMode = 0o600

// OpenAuditLog opens an audit log file for appending, creating it if needed.
func OpenAuditLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, AuditLogMode)
}

// CheckAuditLog checks that UNIFI_AUDIT_LOG can be written, so a mistake is
// reported at startup rather than on the first write.
func CheckAuditLog(path string) error {
	if path == "" || path == AuditLogStderr {
		return nil
	}
	f, err := OpenAuditLog(path)
	if err != nil {
		return fmt.Errorf("UNIFI_AUDIT_LOG: cannot write to %s: %s. Its directory must exist and be writable by the server (in Docker, mount a writable directory; the image runs as UID 65532)", path, errorMessage(err))
	}
	return f.Close()
}

// validURL is a close approximation of `new URL(v)` succeeding: a scheme and,
// for the special schemes, a host.
func validURL(v string) bool {
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss", "ftp":
		return u.Host != ""
	}
	return true
}

var fingerprintPrefix = regexp.MustCompile(`(?i)^sha-?256[:=/ ]?`)
var fingerprintSeparators = regexp.MustCompile(`[:\s-]`)
var fingerprintHex = regexp.MustCompile(`^[0-9A-F]{64}$`)

// NormalizeFingerprint normalises a SHA-256 fingerprint (AA:BB:..., aabb..., optionally prefixed "sha256:") to AA:BB:... form.
func NormalizeFingerprint(value string) (string, error) {
	hex := strings.TrimSpace(value)
	hex = fingerprintPrefix.ReplaceAllString(hex, "")
	hex = strings.ToUpper(fingerprintSeparators.ReplaceAllString(hex, ""))
	if !fingerprintHex.MatchString(hex) {
		return "", fmt.Errorf(`"%s" is not a SHA-256 certificate fingerprint (64 hex digits, optionally colon-separated)`, value)
	}
	pairs := make([]string, 0, 32)
	for i := 0; i < len(hex); i += 2 {
		pairs = append(pairs, hex[i:i+2])
	}
	return strings.Join(pairs, ":"), nil
}

// TLSChoiceRequired is the startup error for an https controller when none of
// UNIFI_VERIFY_SSL, UNIFI_CA_FILE and UNIFI_TLS_FINGERPRINT is set.
const TLSChoiceRequired = "UNIFI_URL is https, but nothing says how to check the controller's certificate. Choose one: " +
	"(1) pin it with UNIFI_TLS_FINGERPRINT (recommended for a console's self-signed certificate; see docs/security.md), " +
	"(2) trust its CA with UNIFI_CA_FILE, or set UNIFI_VERIFY_SSL=true if it has a publicly trusted certificate, or " +
	"(3) set UNIFI_VERIFY_SSL=false to skip the check, which lets anyone on the network path impersonate the controller and capture its credentials"

// PlainHTTPWarning is the startup warning for an http:// controller URL.
func PlainHTTPWarning(u Unifi) string {
	if !strings.HasPrefix(strings.ToLower(u.URL), "http:") {
		return ""
	}
	return "UNIFI_URL uses plain http, so the API key or password and all controller traffic travel unencrypted. Use https (with UNIFI_TLS_FINGERPRINT) unless the connection is on a trusted network."
}

// InsecureTLSWarning is the startup warning for an https controller whose certificate is
// neither verified nor pinned, so anyone on the path could impersonate it and capture credentials.
func InsecureTLSWarning(u Unifi) string {
	if u.VerifySSL || len(u.TLSFingerprints) > 0 || !strings.HasPrefix(strings.ToLower(u.URL), "https:") {
		return ""
	}
	return "the controller's TLS certificate is not verified (UNIFI_VERIFY_SSL=false) and not pinned. Set UNIFI_TLS_FINGERPRINT to pin it (see docs/security.md)."
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// HealthcheckURL is the /health URL the healthcheck command checks, or "" in
// stdio mode. It connects to MCP_HTTP_HOST when that is a specific address, and
// to the loopback address when the server listens on every interface.
func HealthcheckURL(env Env) (string, error) {
	transport, err := read(env, "MCP_TRANSPORT")
	if err != nil {
		return "", err
	}
	if transport == "stdio" {
		return "", nil
	}
	host, err := read(env, "MCP_HTTP_HOST")
	if err != nil {
		return "", err
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	p, err := read(env, "MCP_HTTP_PORT")
	if err != nil {
		return "", err
	}
	port, err := positiveInt("MCP_HTTP_PORT", p, 8080)
	if err != nil {
		return "", err
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/health", nil
}
