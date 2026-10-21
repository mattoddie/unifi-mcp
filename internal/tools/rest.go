package tools

// CRUD helpers for the two configuration API styles:
//   - classic:  /api/s/<site>/rest/<collection>[/<id>]       (responses wrapped in {meta, data})
//   - v2:       /v2/api/site/<site>/<collection>[/<id>]      (plain JSON)
//
// Updates always fetch the current object and PUT the merged result back,
// since several endpoints reject partial objects.

import (
	"context"
	"fmt"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

func enc(value string) (string, error) { return unifi.PathSegment(value, "") }

// restPath is rest/<collection>/<id> with the id encoded as a path segment,
// for ids taken from controller responses.
func restPath(collection string, id any) (string, error) {
	s, ok := id.(string)
	if !ok {
		return "", fmt.Errorf("the controller returned no ID for this %s", collection)
	}
	e, err := unifi.PathSegment(s, "object ID")
	if err != nil {
		return "", err
	}
	return "rest/" + collection + "/" + e, nil
}

// first is Array.isArray(res) ? res[0] : res.
func first(res any) any {
	if a, ok := ojson.Arr(res); ok {
		if len(a) == 0 {
			return U
		}
		return a[0]
	}
	return res
}

func asObj(v any) *Obj {
	o, ok := ojson.AsObj(v)
	if !ok {
		return ojson.New()
	}
	return o
}

func restList(ctx context.Context, c *unifi.Client, site, collection string) ([]any, error) {
	res, err := c.SiteGet(ctx, site, "rest/"+collection, nil)
	if err != nil {
		return nil, err
	}
	return arr(res), nil
}

func restGet(ctx context.Context, c *unifi.Client, site, collection, id string) (*Obj, error) {
	e, err := enc(id)
	if err != nil {
		return nil, err
	}
	res, err := c.SiteGet(ctx, site, "rest/"+collection+"/"+e, nil)
	if err != nil {
		return nil, err
	}
	obj := first(res)
	if !ojson.Truthy(obj) {
		return nil, fmt.Errorf("No %s object with id %s on site %s", collection, id, site)
	}
	return asObj(obj), nil
}

func restCreate(ctx context.Context, c *unifi.Client, site, collection string, body *Obj) (*Obj, error) {
	res, err := c.SitePost(ctx, site, "rest/"+collection, body)
	if err != nil {
		return nil, err
	}
	return asObj(ojson.Coalesce(first(res), body)), nil
}

func restUpdate(ctx context.Context, c *unifi.Client, site, collection, id string, patch *Obj) (*Obj, error) {
	current, err := restGet(ctx, c, site, collection, id)
	if err != nil {
		return nil, err
	}
	merged := ojson.Spread(current, patch)
	e, err := enc(id)
	if err != nil {
		return nil, err
	}
	res, err := c.SitePut(ctx, site, "rest/"+collection+"/"+e, merged)
	if err != nil {
		return nil, err
	}
	return asObj(ojson.Coalesce(first(res), merged)), nil
}

func restDelete(ctx context.Context, c *unifi.Client, site, collection, id string) error {
	s, err := enc(site)
	if err != nil {
		return err
	}
	e, err := enc(id)
	if err != nil {
		return err
	}
	_, err = c.Request(ctx, "DELETE", "/api/s/"+s+"/rest/"+collection+"/"+e, nil)
	return err
}

// restSave creates when id is "", otherwise updates.
func restSave(ctx context.Context, c *unifi.Client, site, collection, id string, body *Obj) (*Obj, error) {
	if id != "" {
		return restUpdate(ctx, c, site, collection, id, body)
	}
	return restCreate(ctx, c, site, collection, body)
}

// ---------------------------------------------------------------- v2

func v2List(ctx context.Context, c *unifi.Client, site, collection string) ([]any, error) {
	res, err := c.V2(ctx, "GET", site, collection, U)
	if err != nil {
		return nil, err
	}
	if a, ok := ojson.Arr(res); ok {
		return a, nil
	}
	return arr(ojson.Coalesce(ojson.Get(res, "data"), []any{})), nil
}

func v2Get(ctx context.Context, c *unifi.Client, site, collection, id string) (*Obj, error) {
	all, err := v2List(ctx, c, site, collection)
	if err != nil {
		return nil, err
	}
	obj := ojson.Find(all, func(o any) bool {
		return ojson.StrictEq(ojson.Get(o, "_id"), id) || ojson.StrictEq(ojson.Get(o, "id"), id)
	})
	if !ojson.Truthy(obj) {
		return nil, fmt.Errorf("No %s object with id %s on site %s", collection, id, site)
	}
	return asObj(obj), nil
}

func v2Create(ctx context.Context, c *unifi.Client, site, collection string, body *Obj) (any, error) {
	res, err := c.V2(ctx, "POST", site, collection, body)
	if err != nil {
		return nil, err
	}
	return ojson.Coalesce(res, body), nil
}

func v2Update(ctx context.Context, c *unifi.Client, site, collection, id string, patch *Obj) (any, error) {
	current, err := v2Get(ctx, c, site, collection, id)
	if err != nil {
		return nil, err
	}
	merged := ojson.Spread(current, patch)
	e, err := enc(id)
	if err != nil {
		return nil, err
	}
	res, err := c.V2(ctx, "PUT", site, collection+"/"+e, merged)
	if err != nil {
		return nil, err
	}
	return ojson.Coalesce(res, merged), nil
}

func v2Delete(ctx context.Context, c *unifi.Client, site, collection, id string) error {
	e, err := enc(id)
	if err != nil {
		return err
	}
	_, err = c.V2(ctx, "DELETE", site, collection+"/"+e, U)
	return err
}

func v2Save(ctx context.Context, c *unifi.Client, site, collection, id string, body *Obj) (any, error) {
	if id != "" {
		return v2Update(ctx, c, site, collection, id, body)
	}
	return v2Create(ctx, c, site, collection, body)
}

// ---------------------------------------------------------------- site settings

// getSetting returns one site setting: rest/setting has one object per key (mgmt, ips, dpi, country, ...).
func getSetting(ctx context.Context, c *unifi.Client, site, key string) (*Obj, error) {
	res, err := c.SiteGet(ctx, site, "rest/setting", nil)
	if err != nil {
		return nil, err
	}
	all := arr(res)
	obj := ojson.Find(all, func(s any) bool { return ojson.StrictEq(ojson.Get(s, "key"), key) })
	if ojson.IsUndefined(obj) {
		keys := make([]string, len(all))
		for i, s := range all {
			keys[i] = stringOrEmpty(ojson.Get(s, "key"))
		}
		return nil, fmt.Errorf(`Site setting "%s" not found; available: %s`, key, strings.Join(keys, ", "))
	}
	return asObj(obj), nil
}

func updateSetting(ctx context.Context, c *unifi.Client, site, key string, patch *Obj) (*Obj, error) {
	current, err := getSetting(ctx, c, site, key)
	if err != nil {
		return nil, err
	}
	merged := ojson.Spread(current, patch)
	k, err := enc(key)
	if err != nil {
		return nil, err
	}
	id, err := enc(ojson.String(current.Get("_id")))
	if err != nil {
		return nil, err
	}
	// Controller-wide super_* settings (super_mgmt holds auto-backup) are written through
	// set/setting rather than rest/setting, as the UniFi UI does.
	prefix := "rest/setting/"
	if strings.HasPrefix(key, "super_") {
		prefix = "set/setting/"
	}
	res, err := c.SitePut(ctx, site, prefix+k+"/"+id, merged)
	if err != nil {
		return nil, err
	}
	return asObj(ojson.Coalesce(first(res), merged)), nil
}

// stringOrEmpty is what Array.prototype.join writes for a value: "" for null and undefined.
func stringOrEmpty(v any) string {
	if ojson.IsNullish(v) {
		return ""
	}
	return ojson.String(v)
}

// joinValues is list.join(sep).
func joinValues(list []any, sep string) string {
	parts := make([]string, len(list))
	for i, v := range list {
		parts[i] = stringOrEmpty(v)
	}
	return strings.Join(parts, sep)
}
