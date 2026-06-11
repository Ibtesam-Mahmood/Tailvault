package update

import "testing"

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in        string
		ok        bool
		mj, mn, p int
	}{
		{"v0.0.106", true, 0, 0, 106},
		{"0.0.106", true, 0, 0, 106},
		{"1.2.3", true, 1, 2, 3},
		{"1.2.3-rc1", true, 1, 2, 3}, // suffix dropped
		{" 2.0.0 ", true, 2, 0, 0},
		{"dev", false, 0, 0, 0},
		{"", false, 0, 0, 0},
		{"1.2", false, 0, 0, 0},
		{"1.2.x", false, 0, 0, 0},
		{"v-1.0.0", false, 0, 0, 0},
	}
	for _, c := range cases {
		got, ok := parseSemver(c.in)
		if ok != c.ok {
			t.Errorf("parseSemver(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && (got.major != c.mj || got.minor != c.mn || got.patch != c.p) {
			t.Errorf("parseSemver(%q) = %v, want %d.%d.%d", c.in, got, c.mj, c.mn, c.p)
		}
	}
}

func TestNewerAvailable(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.0.105", "0.0.106", true},
		{"0.0.105", "v0.0.106", true},
		{"0.0.106", "0.0.106", false}, // equal → not newer
		{"0.0.106", "0.0.105", false}, // older → not newer
		{"0.1.0", "0.0.99", false},
		{"0.0.99", "0.1.0", true},
		{"dev", "0.0.106", false}, // unknown current → never nag
		{"0.0.105", "dev", false}, // unparseable latest → never nag
		{"0.0.105", "", false},    // missing latest → never nag
	}
	for _, c := range cases {
		if got := NewerAvailable(c.current, c.latest); got != c.want {
			t.Errorf("NewerAvailable(%q,%q)=%v want %v", c.current, c.latest, got, c.want)
		}
	}
}
