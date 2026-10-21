package unifi

// Dry runs and the record of what a tool call sent.
//
// A tool call puts a *Call in its context. Request then records every
// state-changing request it sends (for the audit log) and, in a dry run, stops
// the first one before anything is sent: GETs and requests marked
// Options.Read still go through, so a preview can be built from live state.

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

// Call records the requests made for one tool call.
type Call struct {
	mu          sync.Mutex
	dryRun      bool
	sent        []SentRequest
	fetched     []fetched
	intercepted *Intercepted
	blocked     int
}

// SentRequest is a state-changing request that was sent to the controller.
type SentRequest struct {
	Method string
	Path   string
	// Status is the HTTP status, or 0 when no response arrived.
	Status int
}

// Intercepted is the first state-changing request of a dry run, which was not sent.
type Intercepted struct {
	Method string
	Path   string
	Query  []Param
	Body   any
	// Current is the object the request would change, when the call read it earlier.
	Current any
}

type fetched struct {
	path string
	data any
}

type callKey struct{}

// WithCall returns a context whose requests are recorded in call.
func WithCall(ctx context.Context, call *Call) context.Context {
	return context.WithValue(ctx, callKey{}, call)
}

// CallFrom returns the Call in ctx, or nil.
func CallFrom(ctx context.Context) *Call {
	call, _ := ctx.Value(callKey{}).(*Call)
	return call
}

// SetDryRun makes the call a dry run: no state-changing request is sent.
func (c *Call) SetDryRun() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dryRun = true
}

// DryRun reports whether the call is a dry run.
func (c *Call) DryRun() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dryRun
}

// Sent returns the state-changing requests that were sent.
func (c *Call) Sent() []SentRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]SentRequest(nil), c.sent...)
}

// Intercepted returns the first request a dry run stopped, or nil, and how
// many state-changing requests were stopped in all.
func (c *Call) Intercepted() (*Intercepted, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.intercepted, c.blocked
}

func (c *Call) recordSent(method, path string, status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, SentRequest{method, path, status})
}

func (c *Call) remember(path string, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dryRun {
		c.fetched = append(c.fetched, fetched{path, data})
	}
}

// intercept records a request a dry run stops and returns the error the caller sees.
func (c *Call) intercept(method, path string, opts *Options) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocked++
	if c.intercepted == nil {
		c.intercepted = &Intercepted{Method: method, Path: path, Query: opts.Query, Body: ojson.Clone(opts.Body), Current: c.currentLocked(method, path)}
	}
	return &DryRunError{Method: method, Path: path}
}

// currentLocked finds the object a request changes among the responses fetched
// earlier in the call: the response from the same path, or an object whose _id
// or id is the path's last segment (e.g. a device read from stat/device/<mac>
// and written to rest/device/<id>).
func (c *Call) currentLocked(method, path string) any {
	if method == "GET" {
		return nil
	}
	for i := len(c.fetched) - 1; i >= 0; i-- {
		if c.fetched[i].path == path {
			if a, ok := ojson.Arr(c.fetched[i].data); ok {
				if len(a) == 1 {
					return a[0]
				}
				continue
			}
			if _, ok := ojson.AsObj(c.fetched[i].data); ok {
				return c.fetched[i].data
			}
		}
	}
	last := path[strings.LastIndex(path, "/")+1:]
	if decoded, err := url.PathUnescape(last); err == nil {
		last = decoded
	}
	if last == "" {
		return nil
	}
	for i := len(c.fetched) - 1; i >= 0; i-- {
		for _, o := range candidates(c.fetched[i].data) {
			if ojson.StrictEq(ojson.Get(o, "_id"), last) || ojson.StrictEq(ojson.Get(o, "id"), last) {
				return o
			}
		}
	}
	return nil
}

// candidates are the objects in a response: the response itself, the elements
// of an array, or the elements of a {data: [...]} page.
func candidates(v any) []any {
	if a, ok := ojson.Arr(v); ok {
		return a
	}
	if a, ok := ojson.Arr(ojson.Get(v, "data")); ok {
		return a
	}
	return []any{v}
}

// DryRunError is returned for a request a dry run didn't send.
type DryRunError struct{ Method, Path string }

func (e *DryRunError) Error() string {
	return fmt.Sprintf("dry run: %s %s was not sent", e.Method, e.Path)
}

// Preview describes an intercepted request for the model: what would be sent,
// with secrets redacted, and for an update of an object the call read first,
// the fields that would change instead of the whole body.
func (i *Intercepted) Preview() *ojson.Obj {
	send := ojson.New("method", i.Method, "path", i.Path)
	if len(i.Query) > 0 {
		q := ojson.New()
		for _, p := range i.Query {
			q.Set(p.Key, p.Value)
		}
		send.Set("query", RedactSecrets(q))
	}
	out := ojson.New("dry_run", true, "would_send", send)
	if i.Current != nil && i.Method != "DELETE" && !ojson.IsUndefined(i.Body) {
		out.Set("changes", Diff(i.Current, i.Body))
		return out
	}
	if !ojson.IsUndefined(i.Body) {
		send.Set("body", RedactSecrets(i.Body))
	}
	if i.Current != nil && i.Method == "DELETE" {
		out.Set("current", RedactSecrets(i.Current))
	}
	return out
}

// Diff lists the fields an update would change: changed values (from, to),
// added fields and removed ones. Fields are named by path, such as
// port_overrides[2].poe_mode. Removed top-level fields are only reported when
// body is the whole object (it has the current _id or id), since a partial
// update leaves the fields it doesn't mention alone. Secret values are redacted.
func Diff(current, body any) *ojson.Obj {
	d := &differ{changed: ojson.New(), added: ojson.New(), removed: ojson.New()}
	whole := false
	for _, k := range []string{"_id", "id"} {
		if v := ojson.Get(body, k); !ojson.IsUndefined(v) && ojson.StrictEq(v, ojson.Get(current, k)) {
			whole = true
		}
	}
	d.walk("", "", current, body, whole)
	out := ojson.New()
	for _, g := range []struct {
		name string
		o    *ojson.Obj
	}{{"changed", d.changed}, {"added", d.added}, {"removed", d.removed}} {
		if g.o.Len() > 0 {
			out.Set(g.name, g.o)
		}
	}
	return out
}

type differ struct{ changed, added, removed *ojson.Obj }

// walk compares from and to at path; key is the nearest object key, used to
// decide whether a value is a secret.
func (d *differ) walk(path, key string, from, to any, removals bool) {
	fo, fok := ojson.AsObj(from)
	tob, tok := ojson.AsObj(to)
	if fok && tok {
		for _, k := range tob.Keys() {
			p := joinPath(path, k)
			if !fo.Has(k) {
				d.added.Set(p, redactField(k, tob.Get(k)))
				continue
			}
			d.walk(p, k, fo.Get(k), tob.Get(k), true)
		}
		if removals {
			for _, k := range fo.Keys() {
				if !tob.Has(k) {
					d.removed.Set(joinPath(path, k), redactField(k, fo.Get(k)))
				}
			}
		}
		return
	}
	fa, faok := ojson.Arr(from)
	ta, taok := ojson.Arr(to)
	if faok && taok && len(fa) == len(ta) {
		for i := range ta {
			d.walk(path+"["+strconv.Itoa(i)+"]", key, fa[i], ta[i], true)
		}
		return
	}
	fs, _ := ojson.Stringify(from)
	ts, _ := ojson.Stringify(to)
	if fs != ts {
		d.changed.Set(path, ojson.New("from", redactField(key, from), "to", redactField(key, to)))
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// redactField redacts v as RedactSecrets would if it were the value of field key.
func redactField(key string, v any) any {
	if key == "" {
		return RedactSecrets(v)
	}
	return RedactSecrets(ojson.New(key, v)).(*ojson.Obj).Get(key)
}
