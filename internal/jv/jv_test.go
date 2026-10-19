// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package jv

import (
	"math"
	"testing"
)

// The expected strings were taken from jq 1.7.1.
func TestLiteralFormat(t *testing.T) {
	cases := map[string]string{
		"1.0": "1.0", "1E2": "1E+2", "1e2": "1E+2", "100e-2": "1.00", "0.10": "0.10",
		"-0": "-0", "-0.0": "-0.0", "1e1000": "1E+1000",
		"123456789012345678901234567890": "123456789012345678901234567890",
		"1.5e300":                        "1.5E+300", "0.00001": "0.00001", "1e-7": "1E-7", "3.0e0": "3.0",
		"10000000000000000000": "10000000000000000000", "1e17": "1E+17", "-1e-5": "-0.00001",
		"0": "0", "12": "12", "-3.25": "-3.25", "0.0000001": "1E-7", "0.000001": "0.000001",
		"1e0": "1", "0e5": "0E+5", "0.000000": "0.000000", "0.0000000": "0E-7", "120e-1": "12.0",
	}
	for in, want := range cases {
		if got := string(AppendLiteral(nil, in)); got != want {
			t.Errorf("literal %s: got %s, want %s", in, got, want)
		}
	}
}

func TestFloatFormat(t *testing.T) {
	cases := []struct {
		f    float64
		want string
	}{
		{1.0 / 3, "0.3333333333333333"}, {2.0 / 3, "0.6666666666666666"}, {100.0 / 3, "33.333333333333336"},
		{1e17, "1e+17"}, {0.30000000000000004, "0.30000000000000004"}, {4.5, "4.5"}, {math.Inf(1), "1.7976931348623157e+308"},
		{1e15, "1000000000000000"}, {1e16, "1e+16"}, {0.0001, "0.0001"}, {0.00012, "0.00012"}, {1.5e-5, "1.5e-05"},
		{12345e20, "1.2345e+24"}, {123456789e20, "1.23456789e+28"}, {-2.5e-10, "-2.5e-10"}, {1e100, "1e+100"},
		{5e-324, "5e-324"}, {100, "100"}, {3, "3"}, {1.2345678901234568e29, "123456789012345680000000000000"},
		{1e19, "1e+19"}, {0, "0"}, {math.Copysign(0, -1), "-0"}, {math.NaN(), "null"},
	}
	for _, c := range cases {
		if got := string(AppendFloat(nil, c.f)); got != c.want {
			t.Errorf("float %v: got %s, want %s", c.f, got, c.want)
		}
	}
}

func TestEncodeRawMatchesDecoded(t *testing.T) {
	inputs := []string{
		`{"a":1,"b":[1,2,{"x":[],"y":{}}],"c":"h\u00e9\/\u001f\u007f","d":null,"e":true,"f":1e2}`,
		`[1, 2 ,{"x" : [ ] , "y":{}}]`,
		`{"a":1,"b":2,"a":3}`,
		"\"\x7f\xff\"",
		`"\ud83d\ude00"`,
		`{"k\u0041":1,"kA":2}`,
		`{"":1,"":2}`,
		`{"a":{"x":1,"x":2},"b":[{"y":1},{"y":2,"z":1E2}]}`,
		`{"a":"\n\t\"\\\b\f\r","b":-0.0000001,"c":0.000001,"d":"\u00e9\/"}`,
	}
	for _, in := range inputs {
		v, err := Decode([]byte(in))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		for _, f := range []Format{{Compact: true}, {}, {Tab: true}, {Indent: 4}} {
			a := string(Encode(nil, v, f))
			b := string(Encode(nil, Raw(in), f))
			if a != b {
				t.Errorf("%s %+v:\n decoded %s\n raw     %s", in, f, a, b)
			}
			d := &Doc{}
			if _, err := ValidDoc([]byte(in), SkipSpace([]byte(in), 0), d); err != nil {
				t.Fatal(err)
			}
			if c := string(Encode(nil, d, f)); c != a {
				t.Errorf("%s %+v:\n decoded %s\n doc     %s", in, f, a, c)
			}
		}
	}
	got := string(Encode(nil, Raw(`{"a":1,"b":2,"a":3}`), Format{Compact: true}))
	if got != `{"a":3,"b":2}` {
		t.Errorf("duplicate keys: %s", got)
	}
}

func TestValid(t *testing.T) {
	good := []string{`1`, `-0.5e+3`, `"a\"b"`, `{"a":[1,{"b":null}]}`, `[]`, `{}`, `"\ud83d\ude00"`, "\"\xff\""}
	for _, s := range good {
		if end, err := Valid([]byte(s), 0); err != nil || end != len(s) {
			t.Errorf("%s: end %d err %v", s, end, err)
		}
	}
	bad := map[string]bool{ // value: is it an end-of-input error
		`01`: false, `[1,]`: false, `{"a" 1}`: false, `tru`: true, `"\x"`: false, "\"a\tb\"": false,
		`{"a":`: true, `[1`: true, `"abc`: true, `"\ud800"`: false, `"\ud800x"`: false, `nan`: false, `1.`: true,
		`.5`: false, `+1`: false, `[1]x`: false,
	}
	for s, eof := range bad {
		end, err := Valid([]byte(s), 0)
		if s == `[1]x` {
			if err != nil || end != 3 {
				t.Errorf("%s: %d %v", s, end, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: no error", s)
			continue
		}
		if got := err.(*SyntaxError).EOF; got != eof {
			t.Errorf("%s: EOF %v, want %v (%v)", s, got, eof, err)
		}
	}
}

func TestCompare(t *testing.T) {
	vals := []string{`null`, `false`, `true`, `-1`, `0`, `1.0`, `1.5`, `"a"`, `"b"`, `[]`, `[1]`, `[1,2]`, `{}`, `{"a":1}`, `{"a":2}`, `{"b":0}`}
	for i := range vals {
		for j := range vals {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			a, _ := Decode([]byte(vals[i]))
			if got := Compare(a, Raw(vals[j])); got != want {
				t.Errorf("%s vs %s: %d, want %d", vals[i], vals[j], got, want)
			}
		}
	}
	if Compare(Raw(`9007199254740993`), Raw(`9007199254740992`)) != 1 {
		t.Error("exact literal compare")
	}
	if Compare(Raw(`1.0`), Raw(`1`)) != 0 || Compare(Raw(`100e-2`), Raw(`1`)) != 0 || Compare(Raw(`-0`), Raw(`0`)) != 0 {
		t.Error("equal literals")
	}
	if Compare(Raw(`0.5`), Raw(`5e-1`)) != 0 || Compare(Raw(`-2`), Raw(`-10`)) != 1 || Compare(Raw(`12`), Raw(`120e-1`)) != 0 {
		t.Error("literal order")
	}
}

func TestField(t *testing.T) {
	r := Raw(`{"a":1, "b" : {"c":"x\"y"}, "a\u0062":[1,2], "a":3}`)
	if v, ok := Field(r, "a"); !ok || string(v) != "3" {
		t.Errorf("a: %s %v", v, ok)
	}
	if v, ok := Field(r, "ab"); !ok || string(v) != "[1,2]" {
		t.Errorf("ab: %s %v", v, ok)
	}
	if v, ok := Field(r, "b"); !ok || string(v) != `{"c":"x\"y"}` {
		t.Errorf("b: %s", v)
	}
	if _, ok := Field(r, "z"); ok {
		t.Error("z")
	}
	if Len(r) != 3 {
		t.Errorf("len %d", Len(r))
	}
	if v, ok := Index(Raw(`[1,"\\",[2]]`), 2); !ok || string(v) != "[2]" {
		t.Errorf("index %s", v)
	}
}
