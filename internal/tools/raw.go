package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// deleteLike matches commands and endpoints that remove things even though
// they aren't sent with DELETE, e.g. POST cmd/sitemgr {"cmd":"delete-site"},
// cmd/stamgr forget-sta, cmd/sitemgr revoke-admin or v2 .../batch-delete.
var deleteLike = regexp.MustCompile(`(?i)delete|forget|revoke|remove|destroy|reset`)

// deleteLikeReason is the reason a non-GET raw request counts as a delete, if it does.
func deleteLikeReason(path string, body any) string {
	bodies := []any{body}
	if a, ok := ojson.Arr(body); ok {
		bodies = a
	}
	for _, b := range bodies {
		if cmd, ok := ojson.Get(b, "cmd").(string); ok && deleteLike.MatchString(cmd) {
			return fmt.Sprintf(`command "%s"`, cmd)
		}
	}
	for _, seg := range strings.Split(path, "/") {
		if deleteLike.MatchString(seg) {
			return fmt.Sprintf(`path segment "%s"`, seg)
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
		mode = "DELETE, and commands that delete, forget, revoke, remove, destroy or reset something, are not allowed because delete access is disabled (UNIFI_ALLOW_DELETES=false)."
	}

	c.add(Def{
		Toolset: "raw", Name: "unifi_api_request", Title: "Raw UniFi API request",
		Description: "Call any UniFi Network API endpoint directly, for anything the other tools don't cover. `path` is relative to the Network application, e.g. /api/s/default/stat/ccode, /v2/api/site/default/trafficroutes or /integration/v1/sites (official API, API-key auth). " + mode,
		ReadOnly:    boolPtr(!c.AllowWrites),
		Destructive: boolPtr(c.AllowWrites),
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
