package plan

import "testing"

// TestCPUs checks that a share of CPU time, given as a part of a whole,
// becomes a whole number of CPUs rounded up, and that a part or a whole
// that is not positive means no limit.
func TestCPUs(t *testing.T) {
	cases := []struct {
		part, whole int64
		want        int
	}{
		{part: 200000, whole: 100000, want: 2},
		{part: 150000, whole: 100000, want: 2},
		{part: 50000, whole: 100000, want: 1},
		{part: 4 * NanoCPU, whole: NanoCPU, want: 4},
		{part: NanoCPU / 2, whole: NanoCPU, want: 1},
		{part: 0, whole: 100000, want: 0},
		{part: -1, whole: 100000, want: 0},
		{part: 100000, whole: 0, want: 0},
	}
	for _, c := range cases {
		if got := CPUs(c.part, c.whole); got != c.want {
			t.Errorf("CPUs(%d, %d) = %d; want %d", c.part, c.whole, got, c.want)
		}
	}
}
