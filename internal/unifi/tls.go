package unifi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/mattoddie/unifi-mcp/internal/config"
)

// PinError is returned when the controller's certificate doesn't match UNIFI_TLS_FINGERPRINT.
type PinError struct{ Presented string }

func (e *PinError) Error() string {
	presented := e.Presented
	if presented == "" {
		presented = "(none)"
	}
	return fmt.Sprintf("TLS certificate fingerprint mismatch: the controller presented SHA-256 %s, which is not in UNIFI_TLS_FINGERPRINT", presented)
}

// Fingerprint is the SHA-256 fingerprint of a DER certificate in AA:BB:... form.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// newTransport builds the HTTP transport for controller connections.
//   - UNIFI_VERIFY_SSL and UNIFI_CA_FILE control normal certificate verification
//     (a CA file replaces the system roots, as Node's `ca` option does).
//   - UNIFI_TLS_FINGERPRINT pins the SHA-256 fingerprint of the controller's
//     certificate. The pin is checked in VerifyConnection, which runs on every
//     handshake (resumed ones too) before any request is sent. With a pin the
//     hostname check is skipped, since the pin identifies the exact certificate,
//     but with UNIFI_VERIFY_SSL=true the chain must still verify.
func newTransport(cfg config.Unifi) (*http.Transport, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("Could not read UNIFI_CA_FILE (%s): %w", cfg.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("UNIFI_CA_FILE (%s) contains no PEM certificates", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	if !cfg.VerifySSL {
		tlsCfg.InsecureSkipVerify = true
	}
	if len(cfg.TLSFingerprints) > 0 {
		pins := cfg.TLSFingerprints
		verify := cfg.VerifySSL
		roots := tlsCfg.RootCAs
		tlsCfg.InsecureSkipVerify = true
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return &PinError{}
			}
			leaf := cs.PeerCertificates[0]
			if verify {
				inter := x509.NewCertPool()
				for _, cert := range cs.PeerCertificates[1:] {
					inter.AddCert(cert)
				}
				if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter}); err != nil {
					return err
				}
			}
			if presented := Fingerprint(leaf.Raw); !slices.Contains(pins, presented) {
				return &PinError{Presented: presented}
			}
			return nil
		}
	}
	dialer := &net.Dialer{Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:               nil, // like Node's fetch, ignore HTTP(S)_PROXY for the controller
		DialContext:         dialer.DialContext,
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: time.Duration(cfg.TimeoutMs) * time.Millisecond,
		MaxIdleConns:        16,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
	}, nil
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// errorCode describes a connection error with the code Node would report
// (ECONNREFUSED, ENOTFOUND, DEPTH_ZERO_SELF_SIGNED_CERT, ...), so messages and
// the troubleshooting guide stay the same.
func errorCode(err error) string {
	var dnsErr *net.DNSError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.As(err, &unknownAuthority):
		if c := unknownAuthority.Cert; c != nil && c.CheckSignatureFrom(c) == nil {
			return "DEPTH_ZERO_SELF_SIGNED_CERT"
		}
		return "UNABLE_TO_VERIFY_LEAF_SIGNATURE"
	case errors.As(err, &hostname):
		return "ERR_TLS_CERT_ALTNAME_INVALID"
	case errors.As(err, &invalid):
		if invalid.Reason == x509.Expired {
			return "CERT_HAS_EXPIRED"
		}
		return "CERT_UNTRUSTED: " + invalid.Error()
	case errors.As(err, &dnsErr):
		if dnsErr.IsNotFound {
			return "ENOTFOUND"
		}
		return "EAI_AGAIN"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "ECONNREFUSED"
	case errors.Is(err, syscall.ECONNRESET):
		return "ECONNRESET"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "EHOSTUNREACH"
	case errors.Is(err, syscall.ENETUNREACH):
		return "ENETUNREACH"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "ETIMEDOUT"
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "CERT_UNTRUSTED: " + certErr.Err.Error()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}
