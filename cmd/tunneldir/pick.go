package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"tunneldir/internal/config"
	"tunneldir/internal/status"
)

// matchNames returns the configured tunnels a user-typed name refers to: the
// exact name if it exists, otherwise every tunnel whose name contains it
// (case-insensitively). This lets "web" stand in for "web-staging".
func matchNames(cfg *config.Config, query string) []string {
	if _, ok := cfg.Tunnel(query); ok {
		return []string{query}
	}
	q := strings.ToLower(query)
	var out []string
	for _, n := range cfg.Names() {
		if strings.Contains(strings.ToLower(n), q) {
			out = append(out, n)
		}
	}
	return out
}

// resolveNames expands each typed name to exactly one tunnel. An ambiguous name
// opens the picker on its matches when interactive and is an error otherwise,
// so a partial name never silently acts on more than the user meant.
func resolveNames(cfg *config.Config, names []string, verb string) ([]string, bool) {
	var out []string
	for _, name := range names {
		matches := matchNames(cfg, name)
		switch {
		case len(matches) == 1:
			out = append(out, matches[0])
		case len(matches) == 0:
			fmt.Fprintf(os.Stderr, "no tunnel matches %q (see: tunneldir list)\n", name)
			return nil, false
		case interactive():
			picked := choose(cfg, matches, verb, false)
			if len(picked) == 0 {
				return nil, false
			}
			out = append(out, picked...)
		default:
			fmt.Fprintf(os.Stderr, "%q matches several tunnels: %s\n", name, strings.Join(matches, ", "))
			return nil, false
		}
	}
	return dedupe(out), true
}

// expandNames is resolveNames for read-only commands (status): a partial name
// simply selects every tunnel it matches.
func expandNames(cfg *config.Config, names []string) []string {
	var out []string
	for _, name := range names {
		out = append(out, matchNames(cfg, name)...)
	}
	return dedupe(out)
}

// pickInteractively offers the tunnels relevant to verb (stopped ones for up,
// running ones for down) and returns the user's choice. single limits the
// choice to one tunnel (logs).
func pickInteractively(cfg *config.Config, verb string, single bool) []string {
	var candidates []string
	for _, s := range status.Collect(cfg, cfg.Names()) {
		running := s.Status != status.StatusDown
		if (verb == "up" && running) || (verb == "down" && !running) {
			continue
		}
		candidates = append(candidates, s.Name)
	}
	if len(candidates) == 0 {
		switch verb {
		case "up":
			fmt.Println("all tunnels are already running")
		case "down":
			fmt.Println("no tunnels are running")
		default:
			fmt.Println("no tunnels defined")
		}
		return nil
	}
	return choose(cfg, candidates, verb, single)
}

// choose prints a numbered list of candidates with their status and reads the
// selection from stdin: numbers and ranges ("1 3", "2-4"), "a" for all, or
// empty to cancel.
func choose(cfg *config.Config, candidates []string, verb string, single bool) []string {
	states := status.Collect(cfg, candidates)
	for i, s := range states {
		fmt.Printf("  %2d) %-20s %-28s %s\n", i+1, s.Name, s.Target, s.Status)
	}
	prompt := "Tunnel to " + verb + " (number, Enter to cancel): "
	if !single {
		prompt = "Tunnels to " + verb + ` (e.g. "1 3", "2-4", "a" for all; Enter to cancel): `
	}
	fmt.Print(prompt)

	picked, err := parseSelection(readLine(), len(states))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid selection: %v\n", err)
		return nil
	}
	if single && len(picked) > 1 {
		fmt.Fprintln(os.Stderr, "pick a single tunnel")
		return nil
	}
	out := make([]string, 0, len(picked))
	for _, i := range picked {
		out = append(out, states[i].Name)
	}
	return out
}

// parseSelection turns input like "1 3,5-6" or "a" into 0-based indexes into a
// list of n items. Empty input selects nothing.
func parseSelection(input string, n int) ([]int, error) {
	if input == "a" || input == "all" {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		return all, nil
	}
	var out []int
	seen := map[int]bool{}
	for _, tok := range strings.FieldsFunc(input, func(r rune) bool { return r == ' ' || r == ',' }) {
		lo, hi, isRange := strings.Cut(tok, "-")
		if !isRange {
			hi = lo
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || a < 1 || b > n || a > b {
			return nil, fmt.Errorf("%q (choose 1-%d)", tok, n)
		}
		for i := a - 1; i < b; i++ {
			if !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	return out, nil
}

// stdin is shared by every prompt so no typed-ahead input is lost between them.
var stdin = bufio.NewReader(os.Stdin)

// readLine reads one trimmed line of user input ("" at EOF).
func readLine() string {
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

// interactive reports whether stdin is a terminal, i.e. a prompt can be
// answered. (A character-device check isn't enough: /dev/null is one too.)
func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func dedupe(names []string) []string {
	seen := map[string]bool{}
	out := names[:0:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
