package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The functions below edit tunnels.yaml in place for `tunneldir add/edit/remove`.
// They work on the YAML node tree rather than re-marshalling Config, so the
// user's comments, defaults and key order survive. Every write is validated with
// Load before it replaces the file, so a bad edit can never leave a broken config.

// tunnelYAML is the on-disk shape of a tunnel written by the CLI: optional
// fields are omitted when empty so generated entries stay minimal.
type tunnelYAML struct {
	Name         string        `yaml:"name"`
	Host         string        `yaml:"host"`
	User         string        `yaml:"user,omitempty"`
	Port         int           `yaml:"port,omitempty"`
	IdentityFile string        `yaml:"identity_file,omitempty"`
	Autostart    bool          `yaml:"autostart,omitempty"`
	Forwards     []forwardYAML `yaml:"forwards"`
}

type forwardYAML struct {
	Local   string `yaml:"local,omitempty"`
	Remote  string `yaml:"remote,omitempty"`
	Dynamic string `yaml:"dynamic,omitempty"`
}

// keyOrder is the canonical field order, used to place keys a tunnel didn't
// have before (e.g. adding a user to an existing entry).
var keyOrder = []string{"name", "host", "user", "port", "identity_file", "autostart", "forwards"}

// RawTunnel returns a tunnel exactly as written in the file — no defaults
// applied and ~ not expanded — so an edit is pre-filled with what the user wrote.
func RawTunnel(path, name string) (Tunnel, error) {
	_, seq, err := loadDoc(path)
	if err != nil {
		return Tunnel{}, err
	}
	item := findTunnel(seq, name)
	if item == nil {
		return Tunnel{}, fmt.Errorf("unknown tunnel %q", name)
	}
	var t Tunnel
	err = item.Decode(&t)
	return t, err
}

// AddTunnel appends t to the config at path.
func AddTunnel(path string, t Tunnel) error {
	doc, seq, err := loadDoc(path)
	if err != nil {
		return err
	}
	item, err := tunnelNode(t)
	if err != nil {
		return err
	}
	seq.Style = 0 // an empty `tunnels: []` would otherwise stay in flow style
	seq.Content = append(seq.Content, item)
	return saveDoc(path, doc)
}

// UpdateTunnel replaces the tunnel called name with t. Only the fields that
// changed are rewritten, so comments on untouched fields are kept.
func UpdateTunnel(path, name string, t Tunnel) error {
	doc, seq, err := loadDoc(path)
	if err != nil {
		return err
	}
	item := findTunnel(seq, name)
	if item == nil {
		return fmt.Errorf("unknown tunnel %q", name)
	}
	updated, err := tunnelNode(t)
	if err != nil {
		return err
	}

	want := map[string]*yaml.Node{}
	for i := 0; i+1 < len(updated.Content); i += 2 {
		want[updated.Content[i].Value] = updated.Content[i+1]
	}
	// Update or drop the keys the entry already has.
	var kept []*yaml.Node
	for i := 0; i+1 < len(item.Content); i += 2 {
		key, val := item.Content[i], item.Content[i+1]
		newVal, ok := want[key.Value]
		if !ok {
			if isKnownKey(key.Value) {
				continue // the field was cleared
			}
			kept = append(kept, key, val)
			continue
		}
		delete(want, key.Value)
		if !sameValue(val, newVal) {
			newVal.LineComment = val.LineComment
			val = newVal
		}
		kept = append(kept, key, val)
	}
	item.Content = kept
	// Insert keys the entry didn't have, at their canonical position.
	for _, k := range keyOrder {
		if v, ok := want[k]; ok {
			insertKey(item, k, v)
		}
	}
	return saveDoc(path, doc)
}

// RemoveTunnel deletes the tunnel called name, along with the comment above it.
func RemoveTunnel(path, name string) error {
	doc, seq, err := loadDoc(path)
	if err != nil {
		return err
	}
	for i, item := range seq.Content {
		if tunnelName(item) == name {
			seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
			return saveDoc(path, doc)
		}
	}
	return fmt.Errorf("unknown tunnel %q", name)
}

// loadDoc parses path into a node tree and returns it with the `tunnels`
// sequence, creating an empty one if the key is missing or null.
func loadDoc(path string) (*yaml.Node, *yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if doc.Kind == 0 { // empty file
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("%s: top level must be a mapping", path)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "tunnels" {
			continue
		}
		val := root.Content[i+1]
		if val.Kind != yaml.SequenceNode {
			if val.Tag != "!!null" {
				return nil, nil, fmt.Errorf("%s: tunnels must be a list", path)
			}
			seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			root.Content[i+1] = seq
			return &doc, seq, nil
		}
		return &doc, val, nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "tunnels"}, seq)
	return &doc, seq, nil
}

// saveDoc encodes doc and atomically replaces path with it, but only after the
// result loads as a valid config. The file's permissions are preserved.
func saveDoc(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_ = enc.Close()

	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tunnels-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(spaceOut(buf.Bytes())); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	if _, err := Load(tmp.Name()); err != nil {
		return fmt.Errorf("not saved, the result would be invalid: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}

// spaceOut restores the blank lines the YAML encoder drops: one before each
// top-level key and each top-level list item (e.g. each tunnel), placed above
// the comment block that belongs to it. It never adds one directly under a
// parent key, so the first item of a list stays attached to it.
func spaceOut(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	var out []string
	for _, line := range lines {
		isBlock := line != "" && line[0] != ' ' && line[0] != '#' && line[0] != '-'
		isItem := strings.HasPrefix(line, "  - ")
		if (isBlock || isItem) && len(out) > 0 {
			// Walk back over the comment block that heads this line.
			j := len(out)
			for j > 0 && strings.HasPrefix(strings.TrimSpace(out[j-1]), "#") {
				j--
			}
			if j > 0 && out[j-1] != "" && !strings.HasSuffix(out[j-1], ":") {
				out = append(out[:j], append([]string{""}, out[j:]...)...)
			}
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

func tunnelNode(t Tunnel) (*yaml.Node, error) {
	ty := tunnelYAML{
		Name: t.Name, Host: t.Host, User: t.User, Port: t.Port,
		IdentityFile: t.IdentityFile, Autostart: t.Autostart,
	}
	if ty.Port == 22 {
		ty.Port = 0 // the default; don't clutter the entry with it
	}
	for _, f := range t.Forwards {
		ty.Forwards = append(ty.Forwards, forwardYAML(f))
	}
	var n yaml.Node
	if err := n.Encode(ty); err != nil {
		return nil, err
	}
	unquotePorts(&n)
	return &n, nil
}

// unquotePorts writes a bare-port `dynamic` value as a number (dynamic: 1080),
// as a person would, instead of the quoted string the encoder produces.
func unquotePorts(n *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value == "dynamic" && v.Kind == yaml.ScalarNode {
			if _, err := strconv.Atoi(v.Value); err == nil {
				v.Tag, v.Style = "!!int", 0
			}
		}
	}
	for _, c := range n.Content {
		unquotePorts(c)
	}
}

func findTunnel(seq *yaml.Node, name string) *yaml.Node {
	for _, item := range seq.Content {
		if tunnelName(item) == name {
			return item
		}
	}
	return nil
}

func tunnelName(item *yaml.Node) string {
	for i := 0; i+1 < len(item.Content); i += 2 {
		if item.Content[i].Value == "name" {
			return item.Content[i+1].Value
		}
	}
	return ""
}

func isKnownKey(k string) bool {
	for _, known := range keyOrder {
		if k == known {
			return true
		}
	}
	return false
}

// sameValue reports whether two value nodes hold the same data, ignoring style
// and comments, so an unchanged field keeps its original formatting.
func sameValue(a, b *yaml.Node) bool {
	var va, vb any
	if a.Decode(&va) != nil || b.Decode(&vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// insertKey adds key: val to mapping m before the first existing key that
// comes later in keyOrder, or at the end.
func insertKey(m *yaml.Node, key string, val *yaml.Node) {
	rank := func(k string) int {
		for i, known := range keyOrder {
			if k == known {
				return i
			}
		}
		return len(keyOrder)
	}
	pos := len(m.Content)
	for i := 0; i+1 < len(m.Content); i += 2 {
		if rank(m.Content[i].Value) > rank(key) {
			pos = i
			break
		}
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	m.Content = append(m.Content[:pos], append([]*yaml.Node{k, val}, m.Content[pos:]...)...)
}
