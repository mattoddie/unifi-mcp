package ojson

import (
	"math"
	"testing"
)

func TestRoundTripKeepsOrder(t *testing.T) {
	for _, s := range []string{
		`{"b":1,"a":{"z":[1,2,{"y":null}],"c":"x"},"m":true}`,
		`[]`, `{}`, `"str"`, `null`, `[1,"a",false]`,
	} {
		v, err := Parse([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := Stringify(v); got != s {
			t.Errorf("%s -> %s", s, got)
		}
	}
	// Array-index keys come first, as in JavaScript; duplicates keep their first position.
	v := MustParse(`{"b":1,"2":2,"a":3,"1":4,"b":5}`)
	if got, _ := Stringify(v); got != `{"1":4,"2":2,"b":5,"a":3}` {
		t.Errorf("key order: %s", got)
	}
	for _, bad := range []string{``, `{`, `{"a":1} x`, `[1,]`, `nope`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestStringifyLikeJavaScript(t *testing.T) {
	o := New("s", "<a>& \"\\\n\x01é", "n", 1e21, "f", 0.1, "i", 5, "neg", -0.0, "small", 1.5e-7, "nan", math.NaN(), "u", Undefined, "arr", []any{Undefined, nil}, "empty", New(), "list", []any{})
	want := "{\n  \"s\": \"<a>& \\\"\\\\\\n\\u0001é\",\n  \"n\": 1e+21,\n  \"f\": 0.1,\n  \"i\": 5,\n  \"neg\": 0,\n  \"small\": 1.5e-7,\n  \"nan\": null,\n  \"arr\": [\n    null,\n    null\n  ],\n  \"empty\": {},\n  \"list\": []\n}"
	if got := StringifyIndent(o); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if StringifyIndent(Undefined) != "null" {
		t.Error("undefined")
	}
	if _, ok := Stringify(Undefined); ok {
		t.Error("undefined is not JSON")
	}
	if got, _ := Stringify(map[string]any{"b": 1, "a": []string{"x"}}); got != `{"a":["x"],"b":1}` {
		t.Errorf("Go values: %s", got)
	}
}

func TestSemantics(t *testing.T) {
	for _, c := range []struct {
		v      any
		truthy bool
	}{{nil, false}, {Undefined, false}, {"", false}, {0.0, false}, {math.NaN(), false}, {false, false}, {"0", true}, {[]any{}, true}, {New(), true}, {-1.0, true}} {
		if Truthy(c.v) != c.truthy {
			t.Errorf("Truthy(%v)", c.v)
		}
	}
	if Coalesce(nil, Undefined, false, 1.0) != false || !IsUndefined(Coalesce(Undefined, Undefined)) {
		t.Error("Coalesce")
	}
	if !StrictEq(1, 1.0) || StrictEq("1", 1.0) || StrictEq(nil, Undefined) || !StrictEq(Undefined, Undefined) {
		t.Error("StrictEq")
	}
	for in, want := range map[any]float64{"5.1": 5.1, " 7 ": 7, "": 0, "0x10": 16, "1e3": 1000, true: 1, nil: 0} {
		if got := ToNumber(in); got != want {
			t.Errorf("ToNumber(%v) = %v", in, got)
		}
	}
	if !math.IsNaN(ToNumber("abc")) || !math.IsNaN(ToNumber(Undefined)) || !math.IsNaN(ToNumber(New())) {
		t.Error("NaN cases")
	}
	for in, want := range map[string]float64{"42": 42, " 42abc": 42, "-3": -3, "1e3": 1} {
		if got, ok := ParseInt(in); !ok || got != want {
			t.Errorf("ParseInt(%q) = %v", in, got)
		}
	}
	if _, ok := ParseInt("abc"); ok {
		t.Error("ParseInt(abc)")
	}
	for in, want := range map[float64]string{1: "1", 1.5: "1.5", 1e21: "1e+21", 123456789012: "123456789012", 1e-7: "1e-7", 0.000001: "0.000001"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%v) = %s", in, got)
		}
	}
	if Round(2.5) != 3 || Round(-2.5) != -2 || Round(866.05*10)/10 != 866.1 {
		t.Error("Round")
	}
	if String([]any{1.0, nil, "a"}) != "1,,a" || String(New()) != "[object Object]" || String(Undefined) != "undefined" {
		t.Error("String")
	}
	if LocaleCompare("LAN_IN", "LANv6_IN") >= 0 || LocaleCompare("undefined", "WAN_IN") >= 0 || LocaleCompare("a", "A") >= 0 || LocaleCompare("b", "b") != 0 {
		t.Error("LocaleCompare")
	}
}

func TestObjOperations(t *testing.T) {
	a := New("x", 1.0, "y", 2.0)
	b := New("y", 3.0, "z", 4.0)
	if got, _ := Stringify(Spread(a, b, nil, "ignored")); got != `{"x":1,"y":3,"z":4}` {
		t.Errorf("Spread: %s", got)
	}
	a.Set("x", Undefined)
	if a.Has("x") || a.Len() != 1 {
		t.Error("Set Undefined removes")
	}
	c := MustParse(`{"a":{"b":[1,{"c":2}]}}`).(*Obj)
	d := Clone(c).(*Obj)
	Get(d, "a").(*Obj).Set("b", "changed")
	if got, _ := Stringify(c); got != `{"a":{"b":[1,{"c":2}]}}` {
		t.Errorf("Clone is not deep: %s", got)
	}
	if got, _ := Stringify(c.Without("a")); got != `{}` {
		t.Errorf("Without: %s", got)
	}
	var nilObj *Obj
	if !IsUndefined(nilObj.Get("x")) || nilObj.Len() != 0 || !IsUndefined(Get("str", "x")) {
		t.Error("nil-safe Get")
	}
	if n, ok := Len("é😀"); !ok || n != 3 {
		t.Errorf("UTF-16 length %d", n)
	}
}
