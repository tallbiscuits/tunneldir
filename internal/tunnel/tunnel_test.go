package tunnel

import (
	"strings"
	"testing"

	"tunneldir/internal/config"
)

func TestParseForwards(t *testing.T) {
	cases := []struct {
		spec      config.Forward
		wantKind  Kind
		wantPort  int
		wantProbe bool
	}{
		{config.Forward{Local: "8080:localhost:80"}, KindLocal, 8080, true},
		{config.Forward{Local: "127.0.0.1:8080:localhost:80"}, KindLocal, 8080, true},
		{config.Forward{Dynamic: "1080"}, KindDynamic, 1080, true},
		{config.Forward{Dynamic: "0.0.0.0:1080"}, KindDynamic, 1080, true},
		{config.Forward{Remote: "9000:localhost:3000"}, KindRemote, 0, false},
	}
	for _, c := range cases {
		f, err := parseForward(c.spec)
		if err != nil {
			t.Fatalf("%+v: unexpected error %v", c.spec, err)
		}
		if f.Kind != c.wantKind || f.Port != c.wantPort {
			t.Errorf("%+v: got kind=%d port=%d, want kind=%d port=%d", c.spec, f.Kind, f.Port, c.wantKind, c.wantPort)
		}
		if _, ok := f.ProbeAddr(); ok != c.wantProbe {
			t.Errorf("%+v: probeable=%v, want %v", c.spec, ok, c.wantProbe)
		}
	}
}

func TestParseForwardsInvalid(t *testing.T) {
	bad := []config.Forward{
		{Local: "localhost:80"},   // too few parts
		{Local: "x:localhost:80"}, // non-numeric port
		{Dynamic: "notaport"},
	}
	for _, b := range bad {
		if _, err := parseForward(b); err == nil {
			t.Errorf("%+v: expected error, got nil", b)
		}
	}
}

func TestCommand(t *testing.T) {
	tn := config.Tunnel{
		Name: "x", Host: "h.example.com", User: "u", Port: 2222,
		IdentityFile: "/k",
		Forwards:     []config.Forward{{Local: "8080:localhost:80"}, {Dynamic: "1080"}},
	}
	_, args, _, err := Command(tn, config.Defaults{SSHOptions: map[string]string{"Compression": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-N", "-T", "-i /k", "-p 2222", "-L 8080:localhost:80", "-D 1080", "u@h.example.com", "Compression=yes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	// destination must not carry the port (that's via -p)
	if strings.Contains(joined, "h.example.com:2222") {
		t.Errorf("destination should not include :port, got %q", joined)
	}
}
