package signals

import "testing"

// TestParse checks that a signal is recognised by number, by bare name
// and by its "SIG" name in either case, and that an unknown one is an
// error, by parsing each spelling.
func TestParse(t *testing.T) {
	cases := map[string]string{
		"":        "TERM",
		"9":       "KILL",
		"15":      "TERM",
		"KILL":    "KILL",
		"SIGKILL": "KILL",
		"sigint":  "INT",
		"usr1":    "USR1",
	}
	for text, want := range cases {
		got, err := Parse(text)
		if err != nil {
			t.Errorf("Parse(%q): %v", text, err)
			continue
		}
		if got != want {
			t.Errorf("Parse(%q) = %q, want %q", text, got, want)
		}
	}

	for _, text := range []string{"0", "64", "SIGNOPE"} {
		if _, err := Parse(text); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", text)
		}
	}
}

// TestNumber checks that a parsed signal maps back to its number, which
// is what the supervisor's exit status is built from.
func TestNumber(t *testing.T) {
	if got := Number("KILL"); got != 9 {
		t.Errorf("Number(KILL) = %d, want 9", got)
	}
}
