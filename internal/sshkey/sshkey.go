// Package sshkey inspects SSH private key files to decide whether they can be
// used by an unattended process — i.e. one with no ssh-agent and no human to
// type a passphrase, such as a tunnel started by a systemd boot service. A
// passphrase-protected key fails such use silently ("Permission denied
// (publickey)"), so detecting it up front lets the tool warn instead.
//
// Detection parses the key file directly rather than shelling out to
// ssh-keygen, so it never prompts and is unit-testable without external tools.
package sshkey

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"strings"
)

// State is the usability of a private key file for unattended use.
type State int

const (
	// Unknown means the state could not be determined (unrecognised format);
	// callers should not warn, to avoid false positives.
	Unknown State = iota
	// OK means the key is present and not passphrase-protected.
	OK
	// Encrypted means the key is passphrase-protected — unusable without an agent.
	Encrypted
	// Missing means the key file does not exist.
	Missing
)

// Problem reports whether the state should block unattended use.
func (s State) Problem() bool { return s == Encrypted || s == Missing }

// Inspect classifies the private key at path. An empty path is Unknown (the key
// is left to ssh's own defaults/agent, which we can't judge here).
func Inspect(path string) State {
	if path == "" {
		return Unknown
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Missing
		}
		return Unknown
	}
	text := string(data)

	switch {
	// PKCS#8 encrypted key: the header itself says ENCRYPTED.
	case strings.Contains(text, "-----BEGIN ENCRYPTED PRIVATE KEY-----"):
		return Encrypted
	// Legacy PEM (RSA/EC/DSA) encrypted with a passphrase carries these headers.
	case strings.Contains(text, "Proc-Type: 4,ENCRYPTED") || strings.Contains(text, "DEK-Info:"):
		return Encrypted
	// Modern OpenSSH-format key: encryption is recorded in the binary body.
	case strings.Contains(text, "-----BEGIN OPENSSH PRIVATE KEY-----"):
		return inspectOpenSSH(text)
	default:
		return Unknown
	}
}

// inspectOpenSSH decodes an "openssh-key-v1" body far enough to read its cipher
// name: "none" means unencrypted, anything else means passphrase-protected.
func inspectOpenSSH(pem string) State {
	const begin = "-----BEGIN OPENSSH PRIVATE KEY-----"
	const end = "-----END OPENSSH PRIVATE KEY-----"
	i := strings.Index(pem, begin)
	j := strings.Index(pem, end)
	if i < 0 || j < 0 || j <= i {
		return Unknown
	}
	body := strings.Join(strings.Fields(pem[i+len(begin):j]), "")
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return Unknown
	}
	const magic = "openssh-key-v1\x00"
	if !strings.HasPrefix(string(raw), magic) {
		return Unknown
	}
	rest := raw[len(magic):]
	// The first field is a uint32-length-prefixed cipher name.
	if len(rest) < 4 {
		return Unknown
	}
	n := binary.BigEndian.Uint32(rest[:4])
	rest = rest[4:]
	if int(n) > len(rest) {
		return Unknown
	}
	switch string(rest[:n]) {
	case "none":
		return OK
	default:
		return Encrypted
	}
}
