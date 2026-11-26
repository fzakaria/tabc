package dockerapi

import (
	"sort"
	"testing"
)

// TestParseFilters checks that both shapes clients send the filters
// parameter in read as the same thing.
func TestParseFilters(t *testing.T) {
	for _, text := range []string{
		`{"label":["a=b","c"],"name":["web"]}`,
		`{"label":{"a=b":true,"c":true},"name":{"web":true}}`,
	} {
		parsed := parseFilters(text)
		labels := append([]string{}, parsed[filterLabel]...)
		sort.Strings(labels)
		if len(labels) != 2 || labels[0] != "a=b" || labels[1] != "c" || parsed[filterName][0] != "web" {
			t.Errorf("parseFilters(%s) = %v", text, parsed)
		}
	}
	if len(parseFilters("")) != 0 || len(parseFilters("not json")) != 0 {
		t.Error("no filters, or unreadable ones, did not read as none")
	}
}

// TestMatchLabels checks that every label asked for has to be there,
// with its value when one is given.
func TestMatchLabels(t *testing.T) {
	labels := map[string]string{"project": "demo", "service": "web"}

	cases := map[string]bool{
		`{"label":["project=demo"]}`:                true,
		`{"label":["project=demo","service=web"]}`:  true,
		`{"label":["project"]}`:                     true,
		`{"label":["project=other"]}`:               false,
		`{"label":["project=demo","service=nope"]}`: false,
		`{"label":["missing"]}`:                     false,
		`{}`:                                        true,
	}
	for text, want := range cases {
		if got := parseFilters(text).matchLabels(labels); got != want {
			t.Errorf("%s matched: %v, want %v", text, got, want)
		}
	}
}

// TestMatchAny checks that a key with several values is satisfied by
// any one of them, and that a key not filtered on passes everything.
func TestMatchAny(t *testing.T) {
	parsed := parseFilters(`{"status":["running","created"]}`)
	isStatus := func(actual string) func(string) bool {
		return func(value string) bool { return value == actual }
	}

	if !parsed.matchAny(filterStatus, isStatus("created")) {
		t.Error("one of two values did not match")
	}
	if parsed.matchAny(filterStatus, isStatus("exited")) {
		t.Error("a value not asked for matched")
	}
	if !parsed.matchAny(filterName, isStatus("anything")) {
		t.Error("a key that is not filtered on did not pass")
	}
}
