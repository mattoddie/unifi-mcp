package tools

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

// searchArg, limitArg and fieldsArg live in list.go with the list helpers.

func rawArg() Prop {
	return P("raw", Bool().Opt().D("Return the full controller objects (large)"))
}

// secretsArg is the include_secrets argument, offered only with UNIFI_ALLOW_SECRETS.
// Without it the argument isn't in the schema, so it is dropped from calls.
func (c *Context) secretsArg(desc string) []Prop {
	if !c.AllowSecrets {
		return nil
	}
	return []Prop{P("include_secrets", Bool().Opt().D(desc))}
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
