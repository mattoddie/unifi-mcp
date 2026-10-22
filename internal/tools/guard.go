package tools

// Safety layers for tools that change things: a dry_run argument that
// previews the first change without sending it, and an audit log entry for
// every call (UNIFI_AUDIT_LOG).

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

func dryRunArg() Prop {
	return P("dry_run", Bool().Opt().D("Preview only: read the current state and return the first request that would change something (with a field-level diff for updates), without sending it. Nothing is changed."))
}

const dryRunNote = "Nothing was changed: this was a dry run. would_send is the first request that changes something, and it was not sent. Any later steps of the tool, and the controller's response, were not simulated. Call the tool again without dry_run to make the change."

// writeGuard adds dry runs and the audit log to one tool.
type writeGuard struct {
	c     *Context
	name  string
	reads func(Args) bool
}

// guardedCall is what the guard learns about a call while its handler runs.
type guardedCall struct {
	args *Obj
	// read is true when the arguments make this call a read (Def.Reads).
	read bool
}

type guardKey struct{}

// guardWrites gives a tool that changes things the dry_run argument and wraps
// its handler to honour it. The returned guard's wrap adds the audit log entry.
func (c *Context) guardWrites(d *Def) *writeGuard {
	d.Input = append(append([]Prop{}, d.Input...), dryRunArg())
	handler := d.Handler
	d.Handler = func(ctx context.Context, a Args) (any, error) {
		call := unifi.CallFrom(ctx)
		if call == nil {
			call = &unifi.Call{}
			ctx = unifi.WithCall(ctx, call)
		}
		if g, ok := ctx.Value(guardKey{}).(*guardedCall); ok {
			g.args = a.o
			g.read = d.Reads != nil && d.Reads(a)
		}
		dryRun := a.Get("dry_run") == true
		if dryRun {
			call.SetDryRun()
		}
		out, err := handler(ctx, a)
		if !dryRun {
			return out, err
		}
		if req, _ := call.Intercepted(); req != nil {
			preview := req.Preview()
			note := dryRunNote
			if changes, ok := ojson.AsObj(preview.Get("changes")); ok && changes.Len() == 0 {
				note = "The request would not change any field. " + note
			}
			return preview.Set("note", note), nil
		}
		if err != nil {
			return nil, err
		}
		return ojson.New("dry_run", true, "would_send", nil, "note", "The tool would not send any request that changes something. result is what it returned.", "result", out), nil
	}
	return &writeGuard{c: c, name: d.Name}
}

// wrap records each call in the audit log. It wraps the tool's runner rather
// than its handler, so calls refused by input validation are logged too. A nil
// guard returns run unchanged.
func (g *writeGuard) wrap(run runner) runner {
	if g == nil {
		return run
	}
	return func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		call := &unifi.Call{}
		state := &guardedCall{}
		ctx = unifi.WithCall(context.WithValue(ctx, guardKey{}, state), call)
		res := run(ctx, raw)
		if g.c.audit != nil && !state.read {
			g.c.audit.record(g.name, raw, state, call, res)
		}
		return res
	}
}

// auditLog writes one JSON line per call of a tool that changes things.
type auditLog struct {
	target string
	stderr io.Writer
	mu     sync.Mutex
	warned sync.Once
}

func newAuditLog(target string) *auditLog {
	if target == "" {
		return nil
	}
	return &auditLog{target: target, stderr: os.Stderr}
}

func (l *auditLog) record(tool string, raw json.RawMessage, state *guardedCall, call *unifi.Call, res *mcp.CallToolResult) {
	defer func() {
		if r := recover(); r != nil {
			l.warn(r)
		}
	}()
	var args any = U
	if state.args != nil {
		args = state.args
	} else if len(raw) > 0 {
		if parsed, err := ojson.Parse(raw); err == nil {
			args = parsed
		}
	}
	requests := []any{}
	for _, r := range call.Sent() {
		requests = append(requests, ojson.New("method", r.Method, "path", r.Path, "status", ojson.If(r.Status != 0, float64(r.Status), nil)))
	}
	entry := ojson.New(
		"time", time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"tool", tool,
		"dry_run", call.DryRun(),
		"arguments", unifi.RedactSecrets(args),
		"requests", requests,
	)
	if p, _ := call.Intercepted(); p != nil {
		entry.Set("previewed", ojson.New("method", p.Method, "path", p.Path))
	}
	switch {
	case res != nil && res.IsError:
		msg := ""
		if len(res.Content) > 0 {
			if t, ok := res.Content[0].(*mcp.TextContent); ok {
				msg = strings.TrimPrefix(t.Text, "Error: ")
			}
		}
		entry.Set("outcome", "error").Set("error", msg)
	default:
		entry.Set("outcome", "ok")
	}
	line, _ := ojson.Stringify(entry)
	l.write(line + "\n")
}

func (l *auditLog) write(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var err error
	if l.target == config.AuditLogStderr {
		_, err = io.WriteString(l.stderr, line)
	} else {
		var f *os.File
		// Opened for each entry, so a rotated or deleted log is recreated.
		if f, err = config.OpenAuditLog(l.target); err == nil {
			_, err = f.WriteString(line)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err != nil {
		l.warn(err)
	}
}

// warn reports the first audit log failure; tool calls carry on regardless.
func (l *auditLog) warn(err any) {
	l.warned.Do(func() {
		log.Printf("unifi-mcp: WARNING: could not write the audit log (%s): %v. Tool calls carry on; this warning is not repeated.", l.target, err)
	})
}
