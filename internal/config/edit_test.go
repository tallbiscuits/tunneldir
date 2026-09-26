package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# header comment

defaults:
  identity_file: ~/.ssh/id_ed25519

tunnels:
  # the web one
  - name: web
    host: web.example.com
    user: deploy
    autostart: true # boots with the machine
    forwards:
      - local: 8080:localhost:80

  # the socks one
  - name: socks
    host: prod.example.com
    forwards:
      - dynamic: 1080
`

func writeSample(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tunnels.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAddTunnelToEmptyList(t *testing.T) {
	p := writeSample(t, "# keep me\n# example above tunnels\ntunnels: []\n")
	err := AddTunnel(p, Tunnel{Name: "db", Host: "h", Port: 22, Forwards: []Forward{{Local: "5432:localhost:5432"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "# keep me\n# example above tunnels\ntunnels:\n  - name: db\n    host: h\n    forwards:\n      - local: 5432:localhost:5432\n"
	if got := read(t, p); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddTunnelKeepsCommentsAndSpacing(t *testing.T) {
	p := writeSample(t, sample)
	if err := AddTunnel(p, Tunnel{Name: "db", Host: "h", Forwards: []Forward{{Local: "1:h:2"}, {Dynamic: "1081"}}}); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{
		"      - dynamic: 1081\n",
		"# header comment\n\ndefaults:",
		"\n\ntunnels:\n  # the web one\n  - name: web",
		"\n\n  # the socks one\n  - name: socks",
		"      - dynamic: 1080\n\n  - name: db\n",
		"autostart: true # boots with the machine",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestUpdateTunnelChangesOnlyEditedFields(t *testing.T) {
	p := writeSample(t, sample)
	raw, err := RawTunnel(p, "web")
	if err != nil {
		t.Fatal(err)
	}
	if raw.IdentityFile != "" {
		t.Errorf("RawTunnel applied defaults: %q", raw.IdentityFile)
	}
	raw.Name = "web2"
	raw.User = "" // cleared
	raw.Port = 2222
	if err := UpdateTunnel(p, "web", raw); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{
		"  # the web one\n  - name: web2\n    host: web.example.com\n    port: 2222\n    autostart: true # boots with the machine\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "user:") {
		t.Errorf("cleared user still present:\n%s", got)
	}
}

func TestRemoveTunnelTakesItsComment(t *testing.T) {
	p := writeSample(t, sample)
	if err := RemoveTunnel(p, "socks"); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if strings.Contains(got, "socks") {
		t.Errorf("socks still present:\n%s", got)
	}
	if _, err := Load(p); err != nil {
		t.Errorf("result invalid: %v", err)
	}
}

func TestInvalidEditIsNotSaved(t *testing.T) {
	p := writeSample(t, sample)
	err := AddTunnel(p, Tunnel{Name: "web", Host: "dup", Forwards: []Forward{{Local: "1:h:2"}}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
	if read(t, p) != sample {
		t.Error("file changed despite invalid edit")
	}
}
