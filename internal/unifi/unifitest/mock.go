// Package unifitest is a tiny fake UniFi controller covering the endpoints the tests exercise.
package unifitest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

// Request is a recorded request.
type Request struct {
	Method string
	// Path is the raw request target, including /proxy/network and the query string.
	Path   string
	Header http.Header
	// Body is the decoded JSON body, or ojson.Undefined when there was none.
	Body any
}

// Handler answers a matched request with a status and a payload (any JSON value; ojson.Undefined for no body).
type Handler func(body any, match []string, query url.Values) (int, any)

// Options configure the mock.
type Options struct {
	// Mode is "unifi-os" or "legacy".
	Mode   string
	APIKey string
	// TLS serves HTTPS with this certificate instead of plain HTTP.
	TLS *tls.Certificate
}

type route struct {
	method  string
	pattern *regexp.Regexp
	fn      Handler
}

type delay struct {
	pattern *regexp.Regexp
	d       time.Duration
}

// Mock is a fake controller. Every request is recorded.
type Mock struct {
	Server *httptest.Server
	URL    string
	opts   Options

	mu         sync.Mutex
	requests   []Request
	sessions   map[string]bool
	loginCount int
	store      map[string][]any
	handlers   []route
	delays     []delay
	nextID     int
}

// New starts a mock controller. Close it with m.Close().
func New(opts Options) *Mock {
	m := &Mock{opts: opts, sessions: map[string]bool{}, store: map[string][]any{}, nextID: 1}
	m.Seed("rest", "wlanconf", `[{"_id":"w1","name":"Home","enabled":true,"security":"wpapsk","x_passphrase":"hunter22"}]`)
	m.Seed("v2", "trafficrules", `[{"_id":"t1","description":"Block games","enabled":true}]`)
	m.Server = httptest.NewUnstartedServer(http.HandlerFunc(m.serve))
	if opts.TLS != nil {
		m.Server.TLS = &tls.Config{Certificates: []tls.Certificate{*opts.TLS}}
		m.Server.StartTLS()
	} else {
		m.Server.Start()
	}
	m.URL = m.Server.URL
	return m
}

// Close stops the server.
func (m *Mock) Close() { m.Server.Close() }

// On registers a custom route. pattern is matched against the path relative to the
// Network application root without the query string, e.g. ^/api/s/default/stat/ips/event$.
// Custom routes take precedence over built-in ones; the first match wins.
func (m *Mock) On(method, pattern string, fn Handler) *Mock {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers = append(m.handlers, route{method, regexp.MustCompile(pattern), fn})
	return m
}

// Delay slows down responses to paths (relative to the Network application, without query) matching pattern.
func (m *Mock) Delay(pattern string, d time.Duration) *Mock {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delays = append(m.delays, delay{regexp.MustCompile(pattern), d})
	return m
}

// Seed replaces a collection in the generic store, e.g. Seed("rest", "firewallrule", `[{"_id":"r1"}]`).
// items is JSON text or a []any.
func (m *Mock) Seed(kind, collection string, items any) *Mock {
	var list []any
	switch x := items.(type) {
	case string:
		list, _ = ojson.Arr(ojson.MustParse(x))
	default:
		list, _ = ojson.Arr(ojson.Clone(x))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[kind+"/"+collection] = list
	return m
}

// Store returns a collection of the generic store.
func (m *Mock) Store(key string) []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]any(nil), m.store[key]...)
}

// Requests returns the recorded requests.
func (m *Mock) Requests() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Request(nil), m.requests...)
}

// ClearRequests forgets the recorded requests.
func (m *Mock) ClearRequests() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = nil
}

// Last returns the last recorded request.
func (m *Mock) Last() Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return Request{}
	}
	return m.requests[len(m.requests)-1]
}

// RequestsTo returns requests whose path (relative to the Network application) starts with prefix.
func (m *Mock) RequestsTo(prefix string) []Request {
	strip := ""
	if m.opts.Mode == "unifi-os" {
		strip = "/proxy/network"
	}
	var out []Request
	for _, r := range m.Requests() {
		if strings.HasPrefix(r.Path, strip+prefix) {
			out = append(out, r)
		}
	}
	return out
}

// ClearSessions expires every session, as a controller restart would.
func (m *Mock) ClearSessions() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = map[string]bool{}
}

// LoginCount is the number of successful logins.
func (m *Mock) LoginCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loginCount
}

func (m *Mock) relative(path string) string {
	prefix := ""
	if m.opts.Mode == "unifi-os" {
		prefix = "/proxy/network"
	}
	if strings.HasPrefix(path, prefix+"/") {
		path = path[len(prefix):]
	}
	p, _, _ := strings.Cut(path, "?")
	return p
}

func (m *Mock) serve(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body any = ojson.Undefined
	if len(data) > 0 {
		v, err := ojson.Parse(data)
		if err != nil {
			http.Error(w, "bad JSON", http.StatusBadRequest)
			return
		}
		body = v
	}
	path := r.RequestURI
	m.mu.Lock()
	m.requests = append(m.requests, Request{Method: r.Method, Path: path, Header: r.Header.Clone(), Body: body})
	m.mu.Unlock()
	// The response is decided on arrival (so auth uses the session sent), then optionally delayed.
	status, payload, headers := m.route(r, path, body)
	m.mu.Lock()
	var wait time.Duration
	for _, d := range m.delays {
		if d.pattern.MatchString(m.relative(path)) {
			wait = d.d
			break
		}
	}
	m.mu.Unlock()
	time.Sleep(wait)
	w.Header().Set("Content-Type", "application/json")
	for k, vs := range headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	if s, ok := ojson.Stringify(payload); ok {
		_, _ = io.WriteString(w, s)
	}
}

func ok(data any) (int, any) {
	return 200, ojson.New("meta", ojson.New("rc", "ok"), "data", data)
}

// OK is a handler result wrapping data in the classic {meta: {rc: "ok"}, data} envelope.
func OK(data any) (int, any) { return ok(data) }

var sessionCookie = regexp.MustCompile(`(?:TOKEN|unifises)=([^;]+)`)

func (m *Mock) authorized(r *http.Request) bool {
	if m.opts.APIKey != "" {
		return r.Header.Get("X-API-KEY") == m.opts.APIKey
	}
	match := sessionCookie.FindStringSubmatch(r.Header.Get("Cookie"))
	if match == nil {
		return false
	}
	token := match[1]
	m.mu.Lock()
	valid := m.sessions[token]
	m.mu.Unlock()
	if !valid {
		return false
	}
	return r.Method == http.MethodGet || r.Header.Get("X-CSRF-Token") == "csrf-"+token
}

func (m *Mock) route(r *http.Request, path string, body any) (int, any, http.Header) {
	os := m.opts.Mode == "unifi-os"
	if path == "/" {
		if os {
			return 200, ojson.Undefined, nil
		}
		return 302, ojson.Undefined, http.Header{"Location": {"/manage"}}
	}

	loginPath := "/api/login"
	if os {
		loginPath = "/api/auth/login"
	}
	if path == loginPath && r.Method == http.MethodPost {
		m.mu.Lock()
		handlers := append([]route(nil), m.handlers...)
		m.mu.Unlock()
		for _, h := range handlers {
			if h.method == r.Method && h.pattern.MatchString(path) {
				status, payload := h.fn(body, h.pattern.FindStringSubmatch(path), nil)
				return status, payload, nil
			}
		}
		if !ojson.StrictEq(ojson.Get(body, "username"), "admin") || !ojson.StrictEq(ojson.Get(body, "password"), "secret") {
			return 401, ojson.MustParse(`{"meta":{"rc":"error","msg":"api.err.Invalid"}}`), nil
		}
		m.mu.Lock()
		m.loginCount++
		token := fmt.Sprintf("tok%d", m.loginCount)
		m.sessions[token] = true
		m.mu.Unlock()
		if os {
			return 200, ojson.New("username", "admin"), http.Header{
				"Set-Cookie":   {"TOKEN=" + token + "; path=/; HttpOnly"},
				"X-Csrf-Token": {"csrf-" + token},
			}
		}
		return 200, ojson.MustParse(`{"meta":{"rc":"ok"},"data":[]}`), http.Header{
			"Set-Cookie": {"unifises=" + token + "; path=/", "csrf_token=csrf-" + token + "; path=/"},
		}
	}

	prefix := ""
	if os {
		prefix = "/proxy/network"
	}
	if !strings.HasPrefix(path, prefix+"/") {
		return 404, ojson.New("error", "not found"), nil
	}
	if !m.authorized(r) {
		return 401, ojson.MustParse(`{"meta":{"rc":"error","msg":"api.err.LoginRequired"}}`), nil
	}
	p := path[len(prefix):]
	pathOnly, qs, _ := strings.Cut(p, "?")
	query, _ := url.ParseQuery(qs)
	m.mu.Lock()
	handlers := append([]route(nil), m.handlers...)
	m.mu.Unlock()
	for _, h := range handlers {
		if match := h.pattern.FindStringSubmatch(pathOnly); match != nil && h.method == r.Method {
			status, payload := h.fn(body, match, query)
			return status, payload, nil
		}
	}

	switch r.Method + " " + pathOnly {
	case "GET /api/self/sites":
		s, d := ok(ojson.MustParse(`[{"_id":"s1","name":"default","desc":"Default","role":"admin"}]`))
		return s, d, nil
	case "GET /api/s/default/stat/device":
		s, d := ok(Devices())
		return s, d, nil
	case "GET /api/s/default/stat/device/aa:bb:cc:00:00:01":
		s, d := ok([]any{Devices()[0]})
		return s, d, nil
	case "GET /api/s/default/stat/sta":
		s, d := ok(Clients())
		return s, d, nil
	case "GET /api/s/default/stat/user/11:22:33:44:55:66":
		s, d := ok(ojson.MustParse(`[{"_id":"u1","mac":"11:22:33:44:55:66","network_id":"n1","hostname":"laptop"}]`))
		return s, d, nil
	case "PUT /api/s/default/rest/user/u1":
		s, d := ok([]any{ojson.New("_id", "u1", "mac", "11:22:33:44:55:66", "hostname", "laptop").Assign(body)})
		return s, d, nil
	case "GET /api/s/missing/stat/health":
		return 400, ojson.MustParse(`{"meta":{"rc":"error","msg":"api.err.NoSiteContext"},"data":[]}`), nil
	}
	if status, payload, matched := m.generic(r.Method, pathOnly, body); matched {
		return status, payload, nil
	}
	return 404, ojson.New("error", "no mock for "+r.Method+" "+p), nil
}

// generic is CRUD over the in-memory store for /api/s/default/rest/<collection>
// and /v2/api/site/default/<collection> (collections may contain slashes, e.g.
// "firewall/zone"), plus an ok response for any cmd/<manager> POST.
// rest/setting is addressed as rest/setting/<key>/<id>.
func (m *Mock) generic(method, p string, body any) (int, any, bool) {
	if method == http.MethodPost && strings.HasPrefix(p, "/api/s/default/cmd/") {
		s, d := ok([]any{})
		return s, d, true
	}
	var kind, rest string
	switch {
	case strings.HasPrefix(p, "/api/s/default/rest/"):
		kind, rest = "rest", strings.TrimPrefix(p, "/api/s/default/rest/")
	case strings.HasPrefix(p, "/v2/api/site/default/"):
		kind, rest = "v2", strings.TrimPrefix(p, "/v2/api/site/default/")
	default:
		return 0, nil, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var colls []string
	for k := range m.store {
		if c, found := strings.CutPrefix(k, kind+"/"); found && (rest == c || strings.HasPrefix(rest, c+"/")) {
			colls = append(colls, c)
		}
	}
	if len(colls) == 0 {
		return 0, nil, false
	}
	sort.Slice(colls, func(i, j int) bool { return len(colls[i]) > len(colls[j]) })
	coll := colls[0]
	key := kind + "/" + coll
	items := m.store[key]
	var segs []string
	for _, s := range strings.Split(strings.TrimPrefix(rest, coll), "/") {
		if s != "" {
			d, _ := url.PathUnescape(s)
			segs = append(segs, d)
		}
	}
	settingKey := ""
	if kind == "rest" && coll == "setting" && len(segs) > 0 {
		settingKey, segs = segs[0], segs[1:]
	}
	id := ""
	if len(segs) > 0 {
		id = segs[0]
	}

	wrap := func(data any) (int, any, bool) {
		if kind == "rest" {
			if _, isArr := ojson.Arr(data); !isArr {
				data = []any{data}
			}
			s, d := ok(data)
			return s, d, true
		}
		return 200, data, true
	}
	notFound := func() (int, any, bool) {
		if kind == "rest" {
			return 400, ojson.MustParse(`{"meta":{"rc":"error","msg":"api.err.IdInvalid"},"data":[]}`), true
		}
		return 404, ojson.New("message", "not found"), true
	}
	idx := -1
	if id != "" {
		for i, o := range items {
			if ojson.StrictEq(ojson.Get(o, "_id"), id) {
				idx = i
				break
			}
		}
	}

	switch method {
	case http.MethodGet:
		if id == "" {
			if settingKey != "" {
				return wrap(ojson.Filter(items, func(o any) bool { return ojson.StrictEq(ojson.Get(o, "key"), settingKey) }))
			}
			return wrap(append([]any{}, items...))
		}
		if idx < 0 {
			return notFound()
		}
		return wrap(items[idx])
	case http.MethodPost:
		obj := ojson.Spread(body)
		obj.Set("_id", fmt.Sprintf("%024x", m.nextID))
		m.nextID++
		m.store[key] = append(items, obj)
		return wrap(obj)
	case http.MethodPut, http.MethodPatch:
		if idx < 0 {
			return notFound()
		}
		current := ojson.Get(items[idx], "_id")
		updated := ojson.Spread(items[idx], body)
		updated.Set("_id", current)
		items[idx] = updated
		return wrap(updated)
	case http.MethodDelete:
		if idx < 0 {
			return notFound()
		}
		m.store[key] = append(items[:idx:idx], items[idx+1:]...)
		if kind == "rest" {
			s, d := ok([]any{})
			return s, d, true
		}
		return 200, ojson.New(), true
	}
	return 0, nil, false
}

// Devices are the devices the mock reports in stat/device.
func Devices() []any {
	list, _ := ojson.Arr(ojson.MustParse(`[
  {"_id":"d1","mac":"aa:bb:cc:00:00:01","name":"Office Switch","ip":"10.0.0.2","type":"usw","model":"US8P60","version":"7.1.26","state":1,"adopted":true,"uptime":90061,"num_sta":4,
   "system-stats":{"cpu":"5.1","mem":"40.2"},"x_authkey":"very-secret",
   "port_table":[{"port_idx":1,"name":"Port 1","up":true,"enable":true,"speed":1000,"full_duplex":true,"poe_mode":"auto"}]},
  {"_id":"d2","mac":"aa:bb:cc:00:00:02","name":"Lounge AP","type":"uap","model":"U6LR","state":0,"version":"6.6.77"}
]`))
	return list
}

// Clients are the clients the mock reports in stat/sta.
func Clients() []any {
	list, _ := ojson.Arr(ojson.MustParse(`[
  {"mac":"11:22:33:44:55:66","hostname":"laptop","ip":"10.0.0.50","is_wired":false,"essid":"Home","signal":-55,"tx_rate":866000,"uptime":3600},
  {"mac":"11:22:33:44:55:77","name":"NAS","ip":"10.0.0.10","is_wired":true,"sw_mac":"aa:bb:cc:00:00:01","sw_port":3}
]`))
	return list
}

// SelfSignedCert generates a throwaway self-signed certificate for 127.0.0.1
// in memory, so no private key has to be committed. It returns the
// certificate for the server and its PEM encoding (for a CA file).
func SelfSignedCert() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "unifi-mcp test controller"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, certPEM, nil
}
