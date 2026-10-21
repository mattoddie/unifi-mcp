package tools

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// deleteLike matches commands and endpoints that remove things even though
// they aren't sent with DELETE, e.g. POST cmd/sitemgr {"cmd":"delete-site"},
// cmd/stamgr forget-sta, cmd/sitemgr revoke-admin, v2 .../batch-delete or
// cmd/devmgr set-default (a factory reset).
var deleteLike = regexp.MustCompile(`(?i)delete|\bdel-|forget|revoke|remove|destroy|reset|set-default|factory|clear|purge|erase|wipe`)

// classicCmdPath matches classic API command endpoints: /api/s/<site>/cmd/<manager>[/<command>].
var classicCmdPath = regexp.MustCompile(`^/api/s/[^/]+/cmd/([^/]+)(?:/([^/]+))?/?$`)

// knownCommands are the commands of the classic API managers that are known
// not to delete anything. With deletes disabled, a command sent to one of these
// managers must be on its list: the command names aren't documented, so a deny
// list alone can't be relied on to catch every one that removes something.
// Commands for other managers are only checked against deleteLike.
var knownCommands = map[string][]string{
	"stamgr":      {"block-sta", "unblock-sta", "kick-sta", "authorize-guest", "unauthorize-guest"},
	"devmgr":      {"restart", "adopt", "adv-adopt", "force-provision", "set-locate", "unset-locate", "upgrade", "upgrade-external", "set-rollupgrade", "unset-rollupgrade", "power-cycle", "speedtest", "speedtest-status", "spectrum-scan"},
	"sitemgr":     {"add-site", "update-site", "get-admins", "invite-admin", "move-device"},
	"evtmgr":      {"archive-alarm", "archive-all-alarms"},
	"hotspot":     {"create-voucher"},
	"backup":      {"list-backups", "backup"},
	"productinfo": {"check-firmware-update"},
	"firmware":    {"list-available", "list-cached"},
}

// commandsIn returns the string values of every "cmd" key (in any letter case)
// in a request body or, for an array body, in each of its elements.
func commandsIn(body any) []string {
	bodies := []any{body}
	if a, ok := ojson.Arr(body); ok {
		bodies = a
	}
	var cmds []string
	for _, b := range bodies {
		o, ok := ojson.AsObj(b)
		if !ok {
			continue
		}
		for _, k := range o.Keys() {
			if cmd, ok := o.Get(k).(string); ok && strings.EqualFold(k, "cmd") {
				cmds = append(cmds, cmd)
			}
		}
	}
	return cmds
}

// deleteLikeReason is the reason a non-GET raw request counts as a delete, if
// it does. path must already have passed unifi.AssertSafePath; it is checked
// after percent-decoding, so an encoded word can't slip past.
func deleteLikeReason(path string, body any) string {
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	} else {
		return "malformed percent-encoding in the path"
	}
	cmds := commandsIn(body)
	for _, cmd := range cmds {
		if deleteLike.MatchString(cmd) {
			return fmt.Sprintf(`command "%s"`, cmd)
		}
	}
	for _, seg := range strings.Split(path, "/") {
		if deleteLike.MatchString(seg) {
			return fmt.Sprintf(`path segment "%s"`, seg)
		}
	}
	if m := classicCmdPath.FindStringSubmatch(path); m != nil {
		known, ok := knownCommands[strings.ToLower(m[1])]
		if !ok {
			return ""
		}
		if m[2] != "" {
			cmds = append(cmds, m[2])
		}
		for _, cmd := range cmds {
			if !slices.Contains(known, strings.ToLower(cmd)) {
				return fmt.Sprintf(`command "%s" for %s isn't on the list of commands known not to delete anything`, cmd, m[1])
			}
		}
	}
	return ""
}

func registerRaw(c *Context) {
	client := c.Client
	mode := "All HTTP methods are allowed."
	switch {
	case !c.AllowWrites:
		mode = "Only GET is allowed because write access is disabled (UNIFI_ALLOW_WRITES=false)."
	case !c.AllowDeletes:
		mode = "DELETE, and requests that delete, forget, revoke, remove, reset, clear, purge or factory-reset something, are not allowed because delete access is disabled (UNIFI_ALLOW_DELETES=false). Classic cmd/<manager> commands must also be ones known not to delete anything."
	}

	c.add(Def{
		Toolset: "raw", Name: "unifi_api_request", Title: "Raw UniFi API request",
		Description: "Call any UniFi Network API endpoint directly, for anything the other tools don't cover. `path` is relative to the Network application, e.g. /api/s/default/stat/ccode, /v2/api/site/default/trafficroutes or /integration/v1/sites (official API, API-key auth). " + mode,
		ReadOnly:    boolPtr(!c.AllowWrites),
		Destructive: boolPtr(c.AllowWrites),
		Reads: func(a Args) bool {
			m := a.Str("method")
			return m == "" || m == "GET"
		},
		Input: props(
			P("method", Enum("GET", "POST", "PUT", "DELETE", "PATCH").Opt().D("HTTP method (default GET)")),
			P("path", Str().D("Path relative to the Network application, starting with / (no '..' segments or query string)")),
			P("query", Record(Str()).Opt().D("Query string parameters")),
			P("body", Any().Opt().D("JSON request body")),
		),
		Handler: func(ctx context.Context, a Args) (any, error) {
			method := a.Str("method")
			if method == "" {
				method = "GET"
			}
			if method != "GET" && !c.AllowWrites {
				return nil, errorf("Write access is disabled. Set UNIFI_ALLOW_WRITES=true to allow non-GET requests.")
			}
			if method == "DELETE" && !c.AllowDeletes {
				return nil, errorf("Delete access is disabled. Set UNIFI_ALLOW_DELETES=true to allow DELETE requests.")
			}
			path := a.Str("path")
			if err := unifi.AssertSafePath(path); err != nil {
				return nil, err
			}
			if method != "GET" && !c.AllowDeletes {
				if reason := deleteLikeReason(path, a.Get("body")); reason != "" {
					return nil, fmt.Errorf("Delete access is disabled and this request looks like a delete (%s). Set UNIFI_ALLOW_DELETES=true to allow it.", reason)
				}
			}
			var query []unifi.Param
			if q := a.Obj("query"); q != nil {
				for _, k := range q.Keys() {
					query = append(query, unifi.Param{Key: k, Value: q.Get(k)})
				}
			}
			data, err := client.Request(ctx, method, path, &unifi.Options{Query: query, Body: a.Get("body")})
			if err != nil {
				return nil, err
			}
			return unifi.RedactSecrets(data), nil
		},
	})
}
