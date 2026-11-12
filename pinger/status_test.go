package pinger

import "testing"

func TestReceivedPercent(t *testing.T) {
	cases := []struct{ tx, rx, want int }{
		{4, 4, 100}, {40, 40, 100}, {12, 4, 33}, {4, 3, 75}, {300, 1, 1}, {0, 0, 100}, {4, 5, 100},
	}
	for _, c := range cases {
		if got := receivedPercent(c.tx, c.rx); got != c.want {
			t.Errorf("receivedPercent(%v, %v) = %v, want %v", c.tx, c.rx, got, c.want)
		}
	}
}
