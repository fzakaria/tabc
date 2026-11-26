package supervisor

import "testing"

// TestGuestCPUs checks how many CPUs the guest boots with: the default
// when a container asks for none, and never more than the host has
// threads or the kernel has CPUs.
func TestGuestCPUs(t *testing.T) {
	cases := []struct {
		asked, threads, want int
	}{
		{asked: 0, threads: 1, want: 1},
		{asked: 0, threads: 2, want: 2},
		{asked: 2, threads: 1, want: 1},
		{asked: 0, threads: 64, want: defaultCPUs},
		{asked: 1, threads: 64, want: 1},
		{asked: 8, threads: 64, want: 8},
		{asked: 8, threads: 4, want: 4},
		{asked: 64, threads: 256, want: maxCPUs},
	}
	for _, c := range cases {
		if got := guestCPUs(c.asked, c.threads); got != c.want {
			t.Errorf("guestCPUs(%d, %d) = %d; want %d", c.asked, c.threads, got, c.want)
		}
	}
}
