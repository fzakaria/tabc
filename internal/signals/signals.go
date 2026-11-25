// Package signals turns the signal a container engine names, "SIGTERM",
// "TERM" or "15", into the name the guest's process API takes.
package signals

import (
	"fmt"
	"strconv"
	"strings"
)

// prefix is what a signal's name may start with and the guest's API
// leaves out.
const prefix = "SIG"

// Default is the signal `kill` sends when none is named.
const Default = "TERM"

// byNumber is each signal the guest can deliver, at its number on
// Linux's generic ABI, which aarch64 and x86-64 share for these.
var byNumber = map[int]string{
	1: "HUP", 2: "INT", 3: "QUIT", 4: "ILL", 5: "TRAP", 6: "ABRT",
	7: "BUS", 8: "FPE", 9: "KILL", 10: "USR1", 11: "SEGV", 12: "USR2",
	13: "PIPE", 14: "ALRM", 15: "TERM", 17: "CHLD", 18: "CONT",
	19: "STOP", 20: "TSTP", 21: "TTIN", 22: "TTOU", 23: "URG",
	24: "XCPU", 25: "XFSZ", 26: "VTALRM", 27: "PROF", 28: "WINCH",
	29: "IO", 30: "PWR", 31: "SYS",
}

// byName is the same table read the other way.
var byName = func() map[string]int {
	names := make(map[string]int, len(byNumber))
	for number, name := range byNumber {
		names[name] = number
	}
	return names
}()

// Parse returns the guest's name for a signal given by number or by
// name, with or without "SIG" and in either case.
func Parse(text string) (string, error) {
	if text == "" {
		return Default, nil
	}

	// A number names the signal by its position in the table.
	if number, err := strconv.Atoi(text); err == nil {
		name, known := byNumber[number]
		if !known {
			return "", fmt.Errorf("unknown signal number %d", number)
		}
		return name, nil
	}

	// A name is matched without its prefix.
	name := strings.TrimPrefix(strings.ToUpper(text), prefix)
	if _, known := byName[name]; !known {
		return "", fmt.Errorf("unknown signal %q", text)
	}
	return name, nil
}

// Number returns the number of a signal Parse returned.
func Number(name string) int {
	return byName[name]
}
