package dockerapi

import (
	"encoding/json"
	"strings"
)

// paramFilters is the query parameter lists are narrowed by, and the
// keys of it this API reads.
const (
	paramFilters = "filters"
	filterName   = "name"
	filterID     = "id"
	filterLabel  = "label"
	filterStatus = "status"
	filterType   = "type"
	filterEvent  = "event"
	filterCont   = "container"
)

// filters are what a client narrows a list by: for each key, the values
// any one of which will do.
type filters map[string][]string

// parseFilters reads the filters parameter. Clients have written it two
// ways over the years, {"label": ["a=b"]} and {"label": {"a=b": true}},
// and both are still sent.
func parseFilters(text string) filters {
	parsed := filters{}
	if text == "" {
		return parsed
	}

	var lists map[string][]string
	if err := json.Unmarshal([]byte(text), &lists); err == nil {
		return lists
	}
	var sets map[string]map[string]bool
	if err := json.Unmarshal([]byte(text), &sets); err == nil {
		for key, values := range sets {
			for value := range values {
				parsed[key] = append(parsed[key], value)
			}
		}
	}
	return parsed
}

// matchAny reports whether one of a key's values satisfies matches, or
// the key is not filtered on at all.
func (f filters) matchAny(key string, matches func(value string) bool) bool {
	values, filtered := f[key]
	if !filtered || len(values) == 0 {
		return true
	}
	for _, value := range values {
		if matches(value) {
			return true
		}
	}
	return false
}

// matchLabels reports whether labels carry every label the filter asks
// for: "key" for a label that is present, "key=value" for one with that
// value. Unlike the other keys, every label filter has to hold.
func (f filters) matchLabels(labels map[string]string) bool {
	for _, wanted := range f[filterLabel] {
		key, value, hasValue := strings.Cut(wanted, "=")
		actual, present := labels[key]
		if !present || (hasValue && actual != value) {
			return false
		}
	}
	return true
}
