package config

import "testing"

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"5MB", 5 * 1024 * 1024, false},     // 5242880 — frozen binary binding
		{"512KB", 512 * 1024, false},        // 524288
		{"1048576", 1048576, false},         // bare number → bytes
		{"5MiB", 5 * 1024 * 1024, false},    // IEC synonym
		{"1GiB", 1 << 30, false},            // 1073741824
		{"1.5MB", 1572864, false},           // float truncation to int64
		{"100B", 100, false},                // explicit bytes
		{"5 MB", 5 * 1024 * 1024, false},    // optional space
		{"  5mb  ", 5 * 1024 * 1024, false}, // case-insensitive + trim
		{"", 0, true},                       // empty
		{"abc", 0, true},                    // garbage
		{"MB", 0, true},                     // missing number
		{"-5MB", 0, true},                   // negative
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseSize(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSize(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
