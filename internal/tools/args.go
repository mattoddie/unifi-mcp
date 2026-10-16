package tools

import "fmt"

// Arguments shared by many tools.

func siteArg() Prop {
	return P("site", Str().Opt().D("Site name/ID as used in the API (e.g. 'default'). Defaults to the configured site. See unifi_list_sites."))
}

func macSchema() *Schema { return Str().D("MAC address, e.g. aa:bb:cc:dd:ee:ff") }

func macArg() Prop { return P("mac", macSchema()) }

func idSchema(what string) *Schema {
	return Str().D(what + " ID (the `id` field returned by the matching list tool)")
}

func idArg(what string) Prop { return P("id", idSchema(what)) }

// extraSchema is free-form extra fields merged into the object sent to the
// controller, for settings the typed arguments don't cover. Field names are the controller's own.
func extraSchema() *Schema {
	return Record(nil).Opt().D("Additional raw controller fields to set (advanced; field names as in the raw objects). Merged last.")
}

func extraArg() Prop { return P("extra", extraSchema()) }

func searchArg() Prop {
	return P("search", Str().Opt().D("Case-insensitive substring filter on name, hostname, MAC, IP and similar fields"))
}

func limitArg(def int) Prop {
	return P("limit", Int().Positive().Max(5000).Opt().D(fmt.Sprintf("Maximum number of results (default %d)", def)))
}

func rawArg() Prop {
	return P("raw", Bool().Opt().D("Return the full unprocessed objects from the controller (large)"))
}

func secretsArg(desc string) Prop {
	return P("include_secrets", Bool().Opt().D(desc))
}

const secretsPassphrases = "Include secret fields such as passphrases and shared keys (redacted by default)"

func props(ps ...any) []Prop {
	var out []Prop
	for _, p := range ps {
		switch x := p.(type) {
		case Prop:
			out = append(out, x)
		case []Prop:
			out = append(out, x...)
		}
	}
	return out
}
