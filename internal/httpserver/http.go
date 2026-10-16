// Package httpserver serves MCP over Streamable HTTP, with bearer auth, a
// Host/Origin allow-list and a health endpoint.
package httpserver

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
)

// MaxBodyBytes is the largest request body accepted on /mcp.
const MaxBodyBytes = 4 << 20

func writeJSON(w http.ResponseWriter, status int, body any) {
	b, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func rpcError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32000, "message": message}, "id": nil})
}

var bearer = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

func tokenMatches(header, token string) bool {
	presented := ""
	if m := bearer.FindStringSubmatch(header); m != nil {
		presented = m[1]
	}
	return len(presented) == len(token) && subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

var portSuffix = regexp.MustCompile(`:\d+$`)

// originAllowed reports whether the Origin's host (with or without port) is in
// the allow-list. "null" and malformed origins are rejected.
func originAllowed(origin string, allowed []string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return false
	}
	hostname := strings.ToLower(u.Hostname())
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
		hostname = host
	}
	// Like the WHATWG URL parser, drop the scheme's default port.
	if port := u.Port(); port != "" && !(u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443") {
		host += ":" + port
	}
	return slices.Contains(allowed, host) || slices.Contains(allowed, hostname)
}

// Handler serves /mcp (stateless Streamable HTTP: every request is handled on
// its own, so the container can be restarted or scaled freely) and /health.
func Handler(cfg config.HTTP, server *mcp.Server) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Host and Origin checks are done below, against MCP_ALLOWED_HOSTS when it is set.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        MaxBodyBytes,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/healthz":
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		case "/mcp":
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found. The MCP endpoint is /mcp"})
			return
		}

		if len(cfg.AllowedHosts) > 0 {
			hostHeader := strings.ToLower(r.Host)
			hostname := portSuffix.ReplaceAllString(hostHeader, "")
			if !slices.Contains(cfg.AllowedHosts, hostHeader) && !slices.Contains(cfg.AllowedHosts, hostname) {
				rpcError(w, http.StatusForbidden, "Host "+hostHeader+" is not allowed")
				return
			}
			// Browsers send Origin on cross-site requests; non-browser clients usually don't.
			if origins, ok := r.Header["Origin"]; ok {
				origin := origins[0]
				if !originAllowed(origin, cfg.AllowedHosts) {
					rpcError(w, http.StatusForbidden, "Origin "+origin+" is not allowed")
					return
				}
			}
		}
		if cfg.AuthToken != "" && !tokenMatches(r.Header.Get("Authorization"), cfg.AuthToken) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			rpcError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		if r.Method != http.MethodPost {
			// Stateless mode: no server-initiated SSE stream and no sessions to delete.
			w.Header().Set("Allow", "POST")
			rpcError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
		if err != nil {
			rpcError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(body) > MaxBodyBytes {
			rpcError(w, http.StatusBadRequest, "Request body too large")
			return
		}
		if len(bytes.TrimSpace(body)) > 0 && !json.Valid(body) {
			rpcError(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		mcpHandler.ServeHTTP(w, r)
	})
}

// Addr is the listen address.
func Addr(cfg config.HTTP) string {
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

// Serve listens on ln until the server is shut down.
func Serve(srv *http.Server, ln net.Listener, cfg config.HTTP) error {
	auth := " (WARNING: no authentication, MCP_ALLOW_UNAUTHENTICATED=true)"
	if cfg.AuthToken != "" {
		auth = " (bearer auth enabled)"
	}
	log.Printf("unifi-mcp listening on http://%s:%d/mcp%s", cfg.Host, cfg.Port, auth)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Check refuses to serve /mcp without a bearer token unless MCP_ALLOW_UNAUTHENTICATED is set.
func Check(cfg config.HTTP) error {
	if cfg.AuthToken == "" && !cfg.AllowUnauthenticated {
		return errors.New("MCP_AUTH_TOKEN is required for the http transport (or set MCP_ALLOW_UNAUTHENTICATED=true)")
	}
	return nil
}
