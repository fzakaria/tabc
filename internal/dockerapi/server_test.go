package dockerapi

import (
	"reflect"
	"testing"
)

// TestMatch checks the three kinds of pattern segment: a literal, one
// star for a container's id, and two for an image's name, which has
// slashes of its own.
func TestMatch(t *testing.T) {
	cases := []struct {
		pattern []string
		path    []string
		args    []string
		ok      bool
	}{
		{[]string{"_ping"}, []string{"_ping"}, nil, true},
		{[]string{"containers", "*", "json"}, []string{"containers", "abc", "json"}, []string{"abc"}, true},
		{[]string{"containers", "*", "json"}, []string{"containers", "json"}, nil, false},
		{[]string{"containers", "*"}, []string{"containers", "abc", "json"}, nil, false},
		{[]string{"images", "**", "json"}, []string{"images", "ghcr.io", "a", "b:1", "json"}, []string{"ghcr.io/a/b:1"}, true},
		{[]string{"images", "**", "json"}, []string{"images", "alpine", "json"}, []string{"alpine"}, true},
		{[]string{"images", "**", "json"}, []string{"images", "json"}, nil, false},
	}
	for _, c := range cases {
		args, ok := match(c.pattern, c.path)
		if ok != c.ok || (ok && !reflect.DeepEqual(args, c.args)) {
			t.Errorf("match(%v, %v) = %v, %v; want %v, %v", c.pattern, c.path, args, ok, c.args, c.ok)
		}
	}
}
