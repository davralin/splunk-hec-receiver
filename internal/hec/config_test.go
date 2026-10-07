package hec

import "testing"

func TestParseBytes(t *testing.T) {
	tests := map[string]int64{
		"1024": 1024,
		"1KiB": 1024,
		"2MiB": 2 << 20,
		"3GB":  3 << 30,
	}
	for input, expected := range tests {
		actual, err := parseBytes(input)
		if err != nil {
			t.Fatalf("parseBytes(%q): %v", input, err)
		}
		if actual != expected {
			t.Fatalf("parseBytes(%q) = %d, want %d", input, actual, expected)
		}
	}
}
