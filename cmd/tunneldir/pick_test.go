package main

import (
	"reflect"
	"testing"

	"tunneldir/internal/config"
)

func TestParseSelection(t *testing.T) {
	cases := []struct {
		in   string
		want []int
		bad  bool
	}{
		{"", nil, false},
		{"2", []int{1}, false},
		{"1 3,3", []int{0, 2}, false},
		{"2-4", []int{1, 2, 3}, false},
		{"a", []int{0, 1, 2, 3}, false},
		{"5", nil, true},
		{"0", nil, true},
		{"3-2", nil, true},
		{"x", nil, true},
	}
	for _, c := range cases {
		got, err := parseSelection(c.in, 4)
		if (err != nil) != c.bad || !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseSelection(%q) = %v, %v; want %v, bad=%v", c.in, got, err, c.want, c.bad)
		}
	}
}

func TestMatchNames(t *testing.T) {
	cfg := &config.Config{Tunnels: []config.Tunnel{
		{Name: "web-staging"}, {Name: "web-prod"}, {Name: "db"}, {Name: "db-replica"},
	}}
	cases := map[string][]string{
		"db":      {"db"}, // exact name wins over substring matches
		"STAGING": {"web-staging"},
		"web":     {"web-staging", "web-prod"},
		"nope":    nil,
	}
	for q, want := range cases {
		if got := matchNames(cfg, q); !reflect.DeepEqual(got, want) {
			t.Errorf("matchNames(%q) = %v; want %v", q, got, want)
		}
	}
}
