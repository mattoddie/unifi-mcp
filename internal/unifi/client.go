// Package unifi is a minimal client for the UniFi Network application.
package unifi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

// Error is an error from the controller or from talking to it.
type Error struct {
	Message string
	// Status is the HTTP status, when there was a response.
	Status int
}

func (e *Error) Error() string { return e.Message }

// Param is one query string parameter. Undefined values are left out.
type Param struct {
	Key   string
	Value any
}

// Options are the optional parts of a request.
type Options struct {
	// Body is sent as JSON unless it is ojson.Undefined (or the Options are nil).
	Body  any
	Query []Param
	// Read marks a request that only reads even though it isn't a GET, such as
	// a POST to stat/report or a v2 log query. A dry run still sends it, and it
	// isn't recorded as a change. Every other non-GET request is treated as
	// changing state.
	Read bool
}

// MaxResponseBytes is the largest controller response read; anything bigger is an error.
const MaxResponseBytes = 64 << 20

// unifiOSPrefix is where the Network application lives on UniFi OS consoles.
const unifiOSPrefix = "/proxy/network"

// Client talks to a UniFi Network application.
//
// It supports UniFi OS consoles (UDM, UCG, UDR, Cloud Key Gen2+, UniFi OS
// Server), where the Network app lives under /proxy/network, and legacy
// self-hosted Network controllers, where it is served from the root. It
// authenticates with an API key (UniFi OS only) or a local username/password
// session.
type Client struct {
	cfg  config.Unifi
	http *http.Client
	// basePath is the path component of UNIFI_URL (normally empty).
	basePath string

	detectMu sync.Mutex
	mu       sync.Mutex
	unifiOS  *bool
	cookies  []cookie
	csrf     string
	login    *loginCall
	// generation is incremented on every successful login, so a request can tell
	// whether its session is still the current one.
	generation int
	// loginFailures counts consecutive logins the controller rejected, and
	// loginErr and loginRetryAt are the last such failure and when the next
	// attempt is allowed, so bad credentials aren't retried on every call (which
	// could lock the account).
	loginFailures int
	loginErr      error
	loginRetryAt  time.Time
}

type cookie struct{ name, value string }

type loginCall struct {
	done chan struct{}
	err  error
}

// New creates a client. It doesn't contact the controller until the first request.
func New(cfg config.Unifi) (*Client, error) {
	transport, err := newTransport(cfg)
	if err != nil {
		return nil, err
	}
	c := &Client{
		cfg: cfg,
		http: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(cfg.TimeoutMs) * time.Millisecond,
			// Redirects are reported, not followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		basePath: strings.TrimRight(whatwgPathname(cfg.URL), "/"),
	}
	if cfg.APIKey != "" || cfg.ControllerType == config.ControllerUnifiOS {
		yes := true
		c.unifiOS = &yes
	} else if cfg.ControllerType == config.ControllerLegacy {
		no := false
		c.unifiOS = &no
	}
	return c, nil
}

// DefaultSite is UNIFI_SITE.
func (c *Client) DefaultSite() string { return c.cfg.Site }

// Close releases idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// SiteGet GETs a site-scoped classic API endpoint, e.g. SiteGet(ctx, "default", "stat/device", nil).
func (c *Client) SiteGet(ctx context.Context, site, path string, query []Param) (any, error) {
	seg, err := PathSegment(site, "site")
	if err != nil {
		return nil, err
	}
	return c.Request(ctx, "GET", "/api/s/"+seg+"/"+path, &Options{Body: ojson.Undefined, Query: query})
}

// SitePost POSTs to a site-scoped classic API endpoint; a null or undefined body is sent as {}.
func (c *Client) SitePost(ctx context.Context, site, path string, body any) (any, error) {
	seg, err := PathSegment(site, "site")
	if err != nil {
		return nil, err
	}
	if ojson.IsNullish(body) {
		body = ojson.New()
	}
	return c.Request(ctx, "POST", "/api/s/"+seg+"/"+path, &Options{Body: body})
}

// SitePut PUTs to a site-scoped classic API endpoint.
func (c *Client) SitePut(ctx context.Context, site, path string, body any) (any, error) {
	seg, err := PathSegment(site, "site")
	if err != nil {
		return nil, err
	}
	return c.Request(ctx, "PUT", "/api/s/"+seg+"/"+path, &Options{Body: body})
}

// SitePostRead POSTs a query that only reads, such as stat/report or
// stat/event; see Options.Read. A null or undefined body is sent as {}.
func (c *Client) SitePostRead(ctx context.Context, site, path string, body any) (any, error) {
	seg, err := PathSegment(site, "site")
	if err != nil {
		return nil, err
	}
	if ojson.IsNullish(body) {
		body = ojson.New()
	}
	return c.Request(ctx, "POST", "/api/s/"+seg+"/"+path, &Options{Body: body, Read: true})
}

// CmdRead issues a manager command that only reads, such as sitemgr
// get-admins or backup list-backups; see Options.Read.
func (c *Client) CmdRead(ctx context.Context, site, manager, cmd string, params *ojson.Obj) (any, error) {
	seg, err := PathSegment(site, "site")
	if err != nil {
		return nil, err
	}
	return c.Request(ctx, "POST", "/api/s/"+seg+"/cmd/"+manager, &Options{Body: ojson.New("cmd", cmd).Assign(params), Read: true})
}

// V2PostRead POSTs a v2 query that only reads, such as system-log/<class>; see Options.Read.
func (c *Client) V2PostRead(ctx context.Context, site, path string, body any) (any, error) {
	seg, err := PathSegment(site, "site")
	if err != nil {
		return nil, err
	}
	return c.Request(ctx, "POST", "/v2/api/site/"+seg+"/"+path, &Options{Body: body, Read: true})
}

// Cmd issues a command to one of the classic API managers (stamgr, devmgr, hotspot, evtmgr...).
func (c *Client) Cmd(ctx context.Context, site, manager, cmd string, params *ojson.Obj) (any, error) {
	return c.SitePost(ctx, site, "cmd/"+manager, ojson.New("cmd", cmd).Assign(params))
}

// V2 sends a request to the v2 API (/v2/api/site/<site>/...), used by newer features.
func (c *Client) V2(ctx context.Context, method, site, path string, body any) (any, error) {
	seg, err := PathSegment(site, "site")
	if err != nil {
		return nil, err
	}
	return c.Request(ctx, method, "/v2/api/site/"+seg+"/"+path, &Options{Body: body})
}

// Request is the low-level request. path is relative to the Network
// application root (e.g. /api/s/default/stat/health,
// /v2/api/site/default/trafficrules, /integration/v1/sites); the
// /proxy/network prefix is added on UniFi OS. Classic API envelopes
// ({meta, data}) are unwrapped to data. Paths with dot segments (plain or
// percent-encoded) are rejected, so a request can't escape the Network
// application.
func (c *Client) Request(ctx context.Context, method, path string, opts *Options) (any, error) {
	if opts == nil {
		opts = &Options{Body: ojson.Undefined}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if err := AssertSafePath(path); err != nil {
		return nil, err
	}
	call := CallFrom(ctx)
	changes := method != "GET" && !opts.Read
	if changes && call != nil && call.DryRun() {
		// Stopped before logging in, so a dry run never sends anything but
		// reads (and the login those need).
		return nil, call.intercept(method, path, opts)
	}
	if err := c.ensureReady(ctx); err != nil {
		return nil, err
	}

	c.mu.Lock()
	prefix := ""
	if *c.unifiOS {
		prefix = unifiOSPrefix
	}
	generation := c.generation
	c.mu.Unlock()

	send := func() (*response, error) {
		res, err := c.send(ctx, method, prefix+path, opts, prefix)
		if changes && call != nil {
			status := 0
			if res != nil {
				status = res.status
			}
			call.recordSent(method, path, status)
		}
		return res, err
	}
	res, err := send()
	if err != nil {
		return nil, err
	}
	if res.status == 401 && c.cfg.APIKey == "" {
		// Session expired: log in again and retry once. A 403 means the session is
		// valid but not allowed to do this, so logging in again wouldn't help.
		if err := c.reauthenticate(ctx, generation); err != nil {
			return nil, err
		}
		if res, err = send(); err != nil {
			return nil, err
		}
	}
	data, err := c.parse(res, method, path)
	if err == nil && method == "GET" && call != nil {
		call.remember(path, data)
	}
	return data, err
}

// reauthenticate replaces a session the controller rejected. generation is the
// session the failed request was sent with: if another request has logged in
// since, the fresh session is kept instead of being thrown away. Concurrent
// callers share one login.
func (c *Client) reauthenticate(ctx context.Context, generation int) error {
	c.mu.Lock()
	if call := c.login; call != nil {
		c.mu.Unlock()
		<-call.done
		return call.err
	}
	if generation != c.generation {
		c.mu.Unlock()
		return nil
	}
	if err := c.loginBackoffLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	c.cookies = nil
	c.csrf = ""
	call := c.startLoginLocked()
	c.mu.Unlock()
	return c.runLogin(ctx, call)
}

func (c *Client) ensureReady(ctx context.Context) error {
	c.mu.Lock()
	detected := c.unifiOS != nil
	c.mu.Unlock()
	if !detected {
		c.detectMu.Lock()
		c.mu.Lock()
		detected = c.unifiOS != nil
		c.mu.Unlock()
		if !detected {
			os, err := c.detectUnifiOS(ctx)
			if err != nil {
				c.detectMu.Unlock()
				return err
			}
			c.mu.Lock()
			c.unifiOS = &os
			c.mu.Unlock()
		}
		c.detectMu.Unlock()
	}
	if c.cfg.APIKey != "" {
		return nil
	}
	c.mu.Lock()
	if len(c.cookies) > 0 {
		c.mu.Unlock()
		return nil
	}
	if call := c.login; call != nil {
		c.mu.Unlock()
		<-call.done
		return call.err
	}
	if err := c.loginBackoffLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	call := c.startLoginLocked()
	c.mu.Unlock()
	return c.runLogin(ctx, call)
}

// detectUnifiOS checks the console type: UniFi OS answers 200 on "/", legacy controllers redirect to /manage.
func (c *Client) detectUnifiOS(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.cfg.URL+"/", nil)
	if err != nil {
		return false, c.networkError(err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return false, c.networkError(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	return res.StatusCode == 200, nil
}

// startLoginLocked registers a login in flight; c.mu must be held.
func (c *Client) startLoginLocked() *loginCall {
	call := &loginCall{done: make(chan struct{})}
	c.login = call
	return call
}

func (c *Client) runLogin(ctx context.Context, call *loginCall) error {
	// The login is shared, so one caller giving up must not fail the others.
	call.err = c.doLogin(context.WithoutCancel(ctx))
	c.mu.Lock()
	c.login = nil
	var rejected *Error
	if errors.As(call.err, &rejected) && rejected.Status != 0 {
		c.loginFailures++
		c.loginErr = call.err
		c.loginRetryAt = time.Now().Add(loginBackoff(c.loginFailures))
	} else if call.err == nil {
		c.loginFailures, c.loginErr = 0, nil
	}
	c.mu.Unlock()
	close(call.done)
	return call.err
}

// loginBackoff is how long to wait before trying again after the nth consecutive
// rejected login: 5s, 10s, 20s... up to 5 minutes.
func loginBackoff(n int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < n && d < 5*time.Minute; i++ {
		d *= 2
	}
	return min(d, 5*time.Minute)
}

// loginBackoffLocked returns the last login failure while the backoff after it
// lasts; c.mu must be held.
func (c *Client) loginBackoffLocked() error {
	if c.loginErr == nil {
		return nil
	}
	wait := time.Until(c.loginRetryAt)
	if wait <= 0 {
		return nil
	}
	return &Error{Message: fmt.Sprintf("%s (not retrying for %s, to avoid locking the account)", c.loginErr.Error(), wait.Round(time.Second))}
}

var mfaPattern = regexp.MustCompile(`(?i)2fa|mfa|ubic_2fa`)

func (c *Client) doLogin(ctx context.Context) error {
	c.mu.Lock()
	path := "/api/login"
	if *c.unifiOS {
		path = "/api/auth/login"
	}
	c.mu.Unlock()
	body := ojson.New("username", orUndefined(c.cfg.Username), "password", orUndefined(c.cfg.Password), "remember", true)
	res, err := c.send(ctx, "POST", path, &Options{Body: body}, "")
	if err != nil {
		return err
	}
	text := string(res.body)
	if !res.ok() {
		detail := sliceUTF16(text, 300)
		if res.status == 499 || mfaPattern.MatchString(text) {
			detail = "the account requires multi-factor authentication; use a local-only admin account or an API key"
		}
		return &Error{Message: fmt.Sprintf("Login to UniFi controller failed (HTTP %d): %s", res.status, detail), Status: res.status}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cookies) == 0 {
		return &Error{Message: "Login succeeded but the controller did not return a session cookie"}
	}
	c.generation++
	return nil
}

func orUndefined(s string) any {
	if s == "" {
		return ojson.Undefined
	}
	return s
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r *response) ok() bool { return r.status >= 200 && r.status <= 299 }

// send sends a request to path (relative to UNIFI_URL). As a second line of
// defence, it refuses to send anything whose URL, after normalisation, isn't
// exactly path under requiredPrefix.
func (c *Client) send(ctx context.Context, method, path string, opts *Options, requiredPrefix string) (*response, error) {
	if err := AssertSafePath(path); err != nil {
		return nil, err
	}
	resolved := whatwgPathname(c.cfg.URL + path)
	if resolved != c.basePath+path || !strings.HasPrefix(resolved, c.basePath+requiredPrefix+"/") {
		return nil, &Error{Message: fmt.Sprintf("Refusing to send %s %s: it resolves to %s. Percent-encode special characters in the path.", method, path, resolved)}
	}
	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		return nil, c.networkError(err)
	}
	// Send the path exactly as checked, without Go re-encoding it.
	u.Opaque = c.basePath + path
	u.RawPath, u.Path, u.RawQuery, u.Fragment = "", "", "", ""
	var q []string
	for _, p := range opts.Query {
		if !ojson.IsUndefined(p.Value) {
			q = appendParam(q, p.Key, ojson.String(p.Value))
		}
	}
	u.RawQuery = strings.Join(q, "&")

	var body io.Reader
	hasBody := !ojson.IsUndefined(opts.Body)
	if hasBody {
		s, ok := ojson.Stringify(opts.Body)
		if !ok {
			hasBody = false
		} else {
			body = strings.NewReader(s)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.URL, body)
	if err != nil {
		return nil, c.networkError(err)
	}
	req.URL = u
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("X-API-KEY", c.cfg.APIKey)
	} else {
		c.mu.Lock()
		if len(c.cookies) > 0 {
			parts := make([]string, len(c.cookies))
			for i, ck := range c.cookies {
				parts[i] = ck.name + "=" + ck.value
			}
			req.Header.Set("Cookie", strings.Join(parts, "; "))
		}
		if c.csrf != "" {
			req.Header.Set("X-CSRF-Token", c.csrf)
		}
		c.mu.Unlock()
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, c.networkError(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, c.networkError(err)
	}
	if len(data) > MaxResponseBytes {
		return nil, &Error{Message: fmt.Sprintf("%s %s: the controller's response is larger than %d MiB, so it was not read", method, path, MaxResponseBytes>>20)}
	}
	c.captureSession(res.Header)
	return &response{status: res.StatusCode, header: res.Header, body: data}, nil
}

func appendParam(q []string, key, value string) []string {
	// URLSearchParams.set replaces an earlier value for the same key in place.
	prefix := formEncode(key) + "="
	for i, p := range q {
		if strings.HasPrefix(p, prefix) {
			q[i] = prefix + formEncode(value)
			return q
		}
	}
	return append(q, prefix+formEncode(value))
}

var expiredCookie = regexp.MustCompile(`(?i)max-age=0|expires=thu, 01 jan 1970`)

func (c *Client) captureSession(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, line := range h.Values("Set-Cookie") {
		pair, _, _ := strings.Cut(line, ";")
		idx := strings.Index(pair, "=")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(pair[:idx])
		value := strings.TrimSpace(pair[idx+1:])
		if value == "" || expiredCookie.MatchString(line) {
			c.deleteCookieLocked(name)
		} else {
			c.setCookieLocked(name, value)
		}
		if name == "csrf_token" && value != "" {
			c.csrf = value
		}
	}
	csrf := h.Get("X-Updated-Csrf-Token")
	if csrf == "" {
		csrf = h.Get("X-Csrf-Token")
	}
	if csrf != "" {
		c.csrf = csrf
	}
}

func (c *Client) setCookieLocked(name, value string) {
	for i := range c.cookies {
		if c.cookies[i].name == name {
			c.cookies[i].value = value
			return
		}
	}
	c.cookies = append(c.cookies, cookie{name, value})
}

func (c *Client) deleteCookieLocked(name string) {
	for i := range c.cookies {
		if c.cookies[i].name == name {
			c.cookies = append(c.cookies[:i:i], c.cookies[i+1:]...)
			return
		}
	}
}

func (c *Client) parse(res *response, method, path string) (any, error) {
	text := string(res.body)
	var parsed any = ojson.Undefined
	if text != "" {
		if v, err := ojson.Parse(res.body); err == nil {
			parsed = v
		}
	}
	apiMsg := ojson.Coalesce(ojson.Get(parsed, "meta", "msg"), ojson.Get(parsed, "message"), ojson.Get(parsed, "error", "message"), ojson.Get(parsed, "error"))
	if !res.ok() {
		if res.status >= 300 && res.status < 400 {
			location := res.header.Get("Location")
			if location == "" {
				location = "?"
			}
			return nil, &Error{Message: fmt.Sprintf("%s %s was redirected (HTTP %d) to %s; check UNIFI_URL and UNIFI_CONTROLLER_TYPE", method, path, res.status, location), Status: res.status}
		}
		detail, ok := apiMsg.(string)
		if !ok {
			detail = sliceUTF16(text, 300)
		}
		hint := ""
		switch {
		case res.status == 401 || res.status == 403:
			hint = " (check credentials and that the account/API key has sufficient permissions)"
		case res.status == 404:
			hint = " (endpoint not found; it may not exist on this Network application version)"
		}
		return nil, &Error{Message: fmt.Sprintf("%s %s failed with HTTP %d: %s%s", method, path, res.status, detail, hint), Status: res.status}
	}
	if ojson.IsUndefined(parsed) {
		if text == "" {
			return ojson.Undefined, nil
		}
		return nil, &Error{Message: fmt.Sprintf("%s %s returned a non-JSON response: %s", method, path, sliceUTF16(text, 200)), Status: res.status}
	}
	if rc, _ := ojson.Get(parsed, "meta", "rc").(string); rc == "error" {
		msg := ojson.Coalesce(ojson.Get(parsed, "meta", "msg"), "unknown error")
		return nil, &Error{Message: fmt.Sprintf("%s %s failed: %s", method, path, ojson.String(msg)), Status: res.status}
	}
	if o, ok := ojson.AsObj(parsed); ok && o.Has("meta") && o.Has("data") {
		return o.Get("data"), nil
	}
	return parsed, nil
}

// sliceUTF16 is JavaScript's text.slice(0, n), counting UTF-16 code units.
func sliceUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// networkError describes a failure to talk to the controller, with a hint for certificate problems.
func (c *Client) networkError(err error) *Error {
	if isTimeout(err) {
		return &Error{Message: fmt.Sprintf("Timed out after %dms talking to %s", c.cfg.TimeoutMs, c.cfg.URL)}
	}
	var pin *PinError
	if errors.As(err, &pin) {
		return &Error{Message: fmt.Sprintf("Could not reach UniFi controller at %s: %s", c.cfg.URL, pin.Error())}
	}
	cause := errorCode(err)
	hint := ""
	if tlsProblem.MatchString(cause) {
		hint = " (the controller's certificate isn't trusted, usually because it's self-signed; pin it with UNIFI_TLS_FINGERPRINT or trust its CA with UNIFI_CA_FILE, see docs/security.md)"
	}
	return &Error{Message: fmt.Sprintf("Could not reach UniFi controller at %s: %s%s", c.cfg.URL, cause, hint)}
}

var tlsProblem = regexp.MustCompile(`(?i)CERT|SELF_SIGNED|UNABLE_TO_VERIFY|ALTNAME`)

// V2Query is V2 with query string parameters.
func (c *Client) V2Query(ctx context.Context, method, site, path string, query []Param) (any, error) {
	seg, err := PathSegment(site, "site")
	if err != nil {
		return nil, err
	}
	return c.Request(ctx, method, "/v2/api/site/"+seg+"/"+path, &Options{Body: ojson.Undefined, Query: query})
}
