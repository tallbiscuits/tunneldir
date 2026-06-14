package updater

import "testing"

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"1.0.0", "v1.0.0", 0},
		{"v1.0.0", "v1.0.1", -1},
		{"v1.2.0", "v1.1.9", 1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.0", "v1.0.0", 0},
		{"v1.0.0", "v1.0.0-rc1", 0},
		{"dev", "v0.1.0", -1},
		{"v0.1.0", "dev", 1},
	}
	for _, c := range cases {
		if got := compareSemver(c.a, c.b); got != c.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestVerifySHA256(t *testing.T) {
	data := []byte("hello")
	// sha256("hello")
	sums := []byte("2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824  tunneldir-linux-amd64\n")
	if err := verifySHA256(data, sums, "tunneldir-linux-amd64"); err != nil {
		t.Fatalf("expected match, got %v", err)
	}
	if err := verifySHA256([]byte("tampered"), sums, "tunneldir-linux-amd64"); err == nil {
		t.Fatal("expected mismatch error")
	}
	if err := verifySHA256(data, sums, "tunneldir-darwin-arm64"); err == nil {
		t.Fatal("expected missing-checksum error")
	}
}
