package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// dpiCatalog caches the official API's traffic identification names. The v2
// traffic endpoint reports applications and categories as numeric IDs; the
// integration API (API key only) has the names. An application's catalogue ID
// is its category ID << 16 | its application ID.
type dpiCatalog struct {
	mu         sync.Mutex
	categories map[float64]string
	apps       map[float64]string
}

func (d *dpiCatalog) load(ctx context.Context, client *unifi.Client, apps bool) (cats, appNames map[float64]string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fetch := func(path string) (map[float64]string, error) {
		list, err := integrationList(ctx, client, path)
		if err != nil {
			return nil, err
		}
		m := make(map[float64]string, len(list))
		for _, x := range list {
			m[ojson.ToNumber(ojson.Get(x, "id"))] = ojson.String(ojson.Get(x, "name"))
		}
		return m, nil
	}
	if d.categories == nil {
		if d.categories, err = fetch("/integration/v1/dpi/categories"); err != nil {
			return nil, nil, err
		}
	}
	if apps && d.apps == nil {
		if d.apps, err = fetch("/integration/v1/dpi/applications"); err != nil {
			return d.categories, nil, err
		}
	}
	return d.categories, d.apps, nil
}

// dpiRow is one application's (or category's) usage.
type dpiRow struct {
	app, cat       float64
	rx, tx, total  float64
	clients, secs  float64
	hasClients     bool
	hasSecs        bool
	categoryAppSet map[float64]bool // category rows: the apps summed into it
}

func readDPIRow(x any) dpiRow {
	g := func(k string) float64 { return ojson.ToNumber(ojson.Get(x, k)) }
	r := dpiRow{app: g("application"), cat: g("category"), rx: g("bytes_received"), tx: g("bytes_transmitted"), total: g("total_bytes")}
	if v := ojson.Get(x, "client_count"); !ojson.IsNullish(v) {
		r.clients, r.hasClients = ojson.ToNumber(v), true
	}
	if v := ojson.Get(x, "activity_seconds"); !ojson.IsNullish(v) {
		r.secs, r.hasSecs = ojson.ToNumber(v), true
	}
	return r
}

// byCategory sums application rows per category. Client counts can't be
// summed (one client uses many apps), so category rows count apps instead.
func byCategory(rows []dpiRow) []dpiRow {
	idx := map[float64]int{}
	var out []dpiRow
	for _, r := range rows {
		i, ok := idx[r.cat]
		if !ok {
			i = len(out)
			idx[r.cat] = i
			out = append(out, dpiRow{cat: r.cat, categoryAppSet: map[float64]bool{}})
		}
		o := &out[i]
		o.rx += r.rx
		o.tx += r.tx
		o.total += r.total
		o.secs += r.secs
		o.hasSecs = o.hasSecs || r.hasSecs
		o.categoryAppSet[r.app] = true
	}
	return out
}

type dpiNames struct {
	cats, apps map[float64]string
}

func (n dpiNames) cat(id float64) any {
	if s, ok := n.cats[id]; ok && s != "" {
		return s
	}
	return U
}

func (n dpiNames) app(cat, app float64) any {
	if s, ok := n.apps[cat*65536+app]; ok && s != "" {
		return s
	}
	return U
}

// dpiRows sorts rows by total bytes, keeps the top limit and shapes them.
func dpiRows(rows []dpiRow, byApp bool, names dpiNames, limit int) *Obj {
	if !byApp {
		rows = byCategory(rows)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].total > rows[j].total })
	total := 0.0
	for _, r := range rows {
		total += r.total
	}
	all := len(rows)
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	items := make([]any, len(rows))
	for i, r := range rows {
		o := ojson.New()
		if byApp {
			o.Set("application", names.app(r.cat, r.app))
			o.Set("application_id", r.app)
		}
		o.Set("category", names.cat(r.cat))
		o.Set("category_id", r.cat)
		o.Set("rx_bytes", r.rx)
		o.Set("tx_bytes", r.tx)
		o.Set("total_bytes", r.total)
		if byApp && r.hasClients {
			o.Set("clients", r.clients)
		}
		if !byApp {
			o.Set("applications", len(r.categoryAppSet))
		}
		if r.hasSecs {
			o.Set("active", unifi.Duration(r.secs))
		}
		items[i] = unifi.CompactObj(o)
	}
	return ojson.New("total_bytes", total, "count", all, "returned", len(items), "items", items)
}

// v2DPIStats reads /v2/api/site/<site>/traffic (Network 9+), which replaced
// the classic stat/sitedpi and stat/stadpi on newer versions.
func v2DPIStats(ctx context.Context, c *Context, a Args, site string, macList []any) (*Obj, error) {
	client := c.Client
	hours := a.NumOr("hours", 24)
	end := nowMs()
	start := end - hours*3600000
	res, err := client.V2Query(ctx, "GET", site, "traffic", []unifi.Param{
		{Key: "start", Value: start}, {Key: "end", Value: end},
		{Key: "includeTrafficUsage", Value: "true"}, {Key: "includeUnidentified", Value: "true"},
	})
	if err != nil {
		return nil, err
	}
	if _, ok := ojson.Arr(ojson.Get(res, "total_usage_by_app")); !ok {
		return nil, fmt.Errorf("GET traffic returned no total_usage_by_app")
	}
	byApp := a.Str("by") == "app"
	limit := int(a.NumOr("limit", 25))

	var names dpiNames
	var namesErr any = U
	cats, apps, err := c.dpi.load(ctx, client, byApp)
	names = dpiNames{cats, apps}
	if err != nil {
		namesErr = "names unavailable (the official integration API needs UNIFI_API_KEY): " + err.Error()
	}

	out := ojson.New(
		"source", "v2 traffic",
		"by", ojson.If(byApp, "app", "category"),
		"from", unifi.IsoTime(start),
		"to", unifi.IsoTime(end),
		"names_error", namesErr,
	)
	rowsOf := func(list any) []dpiRow {
		items := arr(list)
		rows := make([]dpiRow, len(items))
		for i, x := range items {
			rows[i] = readDPIRow(x)
		}
		return rows
	}
	if len(macList) == 0 {
		return unifi.CompactObj(out.Assign(dpiRows(rowsOf(ojson.Get(res, "total_usage_by_app")), byApp, names, limit))), nil
	}
	want := map[string]bool{}
	for _, m := range macList {
		want[strings.ToLower(ojson.String(m))] = true
	}
	clients := []any{}
	for _, cu := range arr(ojson.Get(res, "client_usage_by_app")) {
		cl := ojson.Get(cu, "client")
		m := strings.ToLower(ojson.String(ojson.Get(cl, "mac")))
		if !want[m] {
			continue
		}
		entry := unifi.Compact("mac", m, "name", ojson.Coalesce(ojson.Get(cl, "name"), U))
		clients = append(clients, unifi.CompactObj(entry.Assign(dpiRows(rowsOf(ojson.Get(cu, "usage_by_app")), byApp, names, limit))))
	}
	return unifi.CompactObj(out.Set("clients", clients)), nil
}
