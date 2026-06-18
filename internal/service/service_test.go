package service

import (
	"strings"
	"testing"
)

// TestSystemUnitWorkingDirectoryUnquoted guards against regressing the systemd
// "WorkingDirectory= path is not absolute" failure: unlike the Exec lines and
// Environment=, WorkingDirectory= takes the literal remainder of the line, so
// quoting it makes systemd reject the unit.
func TestSystemUnitWorkingDirectoryUnquoted(t *testing.T) {
	text := SystemUnitText("/home/me/.local/bin/tunneldir", "/home/me/.config/tunneldir/tunnels.yaml", "me", "/home/me")

	if !strings.Contains(text, "\nWorkingDirectory=/home/me\n") {
		t.Errorf("WorkingDirectory should be an unquoted absolute path; got unit:\n%s", text)
	}
	// HOME, by contrast, is an Environment= assignment and must stay quoted.
	if !strings.Contains(text, `Environment="HOME=/home/me"`) {
		t.Errorf("Environment HOME should be quoted; got unit:\n%s", text)
	}
	// Exec lines stay quoted so paths with spaces survive.
	if !strings.Contains(text, `ExecStart="/home/me/.local/bin/tunneldir"`) {
		t.Errorf("ExecStart binary should be quoted; got unit:\n%s", text)
	}
	if !strings.Contains(text, "User=me\n") {
		t.Errorf("expected User=me; got unit:\n%s", text)
	}
}
