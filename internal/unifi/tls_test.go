package unifi_test

import (
	"strings"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

// TestTLS runs against a real HTTPS mock with a certificate generated in memory.
func TestTLS(t *testing.T) {
	cert, certPEM, err := unifitest.SelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	m := unifitest.New(unifitest.Options{Mode: "unifi-os", APIKey: "key123", TLS: &cert})
	defer m.Close()
	fingerprint := unifi.Fingerprint(cert.Leaf.Raw)
	wrongPin := strings.Repeat("00", 32)
	caFile := writeFile(t, "ca.pem", certPEM)

	sites := func(extra ...string) (any, error) {
		env := map[string]string{"UNIFI_URL": m.URL, "UNIFI_API_KEY": "key123"}
		for i := 0; i+1 < len(extra); i += 2 {
			env[extra[i]] = extra[i+1]
		}
		cfg, err := config.Load(config.MapEnv(env))
		if err != nil {
			return nil, err
		}
		c, err := unifi.New(cfg.Unifi)
		if err != nil {
			return nil, err
		}
		defer c.Close()
		return c.Request(ctx, "GET", "/api/self/sites", nil)
	}
	okSites := func(what string, extra ...string) {
		t.Helper()
		res, err := sites(extra...)
		if err != nil {
			t.Errorf("%s: %v", what, err)
			return
		}
		if ojson.Get(res.([]any)[0], "name") != "default" {
			t.Errorf("%s: %v", what, res)
		}
	}
	failSites := func(what, want string, extra ...string) string {
		t.Helper()
		_, err := sites(extra...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (want %q)", what, err, want)
			return ""
		}
		return err.Error()
	}

	// The self-signed certificate is accepted by default (verification off).
	okSites("default")

	// A matching pin is accepted; a mismatched one is refused before anything is sent.
	okSites("pin", "UNIFI_TLS_FINGERPRINT", fingerprint)
	okSites("pin list", "UNIFI_TLS_FINGERPRINT", wrongPin+","+fingerprint)
	before := len(m.Requests())
	msg := failSites("wrong pin", "fingerprint mismatch", "UNIFI_TLS_FINGERPRINT", wrongPin)
	if !strings.Contains(msg, fingerprint) || !strings.HasPrefix(msg, "Could not reach UniFi controller at "+m.URL+": TLS certificate fingerprint mismatch: the controller presented SHA-256 "+fingerprint+", which is not in UNIFI_TLS_FINGERPRINT") {
		t.Errorf("pin message: %s", msg)
	}
	// A pin also applies with verification on.
	failSites("wrong pin with CA", "fingerprint mismatch", "UNIFI_TLS_FINGERPRINT", wrongPin, "UNIFI_CA_FILE", caFile)
	if n := len(m.Requests()); n != before {
		t.Errorf("%d requests sent despite a pin mismatch", n-before)
	}

	// Verification failures suggest pinning.
	msg = failSites("verify", "pin it with UNIFI_TLS_FINGERPRINT", "UNIFI_VERIFY_SSL", "true")
	if strings.Contains(msg, "set UNIFI_VERIFY_SSL=false") || !strings.Contains(msg, "DEPTH_ZERO_SELF_SIGNED_CERT") {
		t.Errorf("verify message: %s", msg)
	}
	// With UNIFI_VERIFY_SSL=true the chain must still verify; a pin alone does not replace a trusted CA.
	failSites("verify with pin", "pin it with UNIFI_TLS_FINGERPRINT", "UNIFI_VERIFY_SSL", "true", "UNIFI_TLS_FINGERPRINT", fingerprint)

	// A custom CA from UNIFI_CA_FILE is trusted, with or without a pin.
	okSites("CA", "UNIFI_CA_FILE", caFile)
	okSites("CA and pin", "UNIFI_CA_FILE", caFile, "UNIFI_TLS_FINGERPRINT", fingerprint)

	// With a CA and no pin, the hostname is still checked.
	localhost := strings.Replace(m.URL, "127.0.0.1", "localhost", 1)
	cfg, _ := config.Load(config.MapEnv(map[string]string{"UNIFI_URL": localhost, "UNIFI_API_KEY": "key123", "UNIFI_CA_FILE": caFile}))
	c, _ := unifi.New(cfg.Unifi)
	defer c.Close()
	if _, err := c.Request(ctx, "GET", "/api/self/sites", nil); err == nil || !strings.Contains(err.Error(), "ERR_TLS_CERT_ALTNAME_INVALID") {
		t.Errorf("hostname check: %v", err)
	}
	// ...but a pin replaces the hostname check, as it identifies the exact certificate.
	cfg, _ = config.Load(config.MapEnv(map[string]string{"UNIFI_URL": localhost, "UNIFI_API_KEY": "key123", "UNIFI_CA_FILE": caFile, "UNIFI_TLS_FINGERPRINT": fingerprint}))
	c2, _ := unifi.New(cfg.Unifi)
	defer c2.Close()
	if _, err := c2.Request(ctx, "GET", "/api/self/sites", nil); err != nil {
		t.Errorf("pin with another hostname: %v", err)
	}
}

func TestFingerprintFormat(t *testing.T) {
	fp := unifi.Fingerprint([]byte("x"))
	if len(fp) != 95 || strings.ToUpper(fp) != fp || strings.Count(fp, ":") != 31 {
		t.Errorf("fingerprint %s", fp)
	}
	n, err := config.NormalizeFingerprint(strings.ToLower(strings.ReplaceAll(fp, ":", "")))
	if err != nil || n != fp {
		t.Errorf("normalize %s %v", n, err)
	}
}
