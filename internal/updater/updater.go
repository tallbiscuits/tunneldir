// Package updater checks GitHub for newer releases and can replace the running
// binary in place. It uses only the standard library and is written to fail
// soft: a failed network check must never break an ordinary command.
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"tunneldir/internal/paths"
)

// repo is the GitHub "owner/name" slug releases are fetched from.
const repo = "tallbiscuits/tunneldir"

// noCheckEnv disables the cached startup check when set to any non-empty value.
const noCheckEnv = "TUNNELDIR_NO_UPDATE_CHECK"

// checkInterval is the minimum time between network checks for CheckCached.
const checkInterval = 24 * time.Hour

// httpTimeout bounds every network request so the tool never hangs on a slow or
// unreachable GitHub.
const httpTimeout = 5 * time.Second

// release is the subset of the GitHub releases API response we care about.
type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// assetName is the release asset for the current platform, matching the names
// produced by build.sh (e.g. "tunneldir-linux-amd64").
func assetName() string {
	return fmt.Sprintf("tunneldir-%s-%s", runtime.GOOS, runtime.GOARCH)
}

func client() *http.Client { return &http.Client{Timeout: httpTimeout} }

// latestRelease fetches the latest published release from GitHub.
func latestRelease() (*release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("no release found")
	}
	return &rel, nil
}

// Check fetches the latest release and reports its tag and whether it is newer
// than current.
func Check(current string) (latest string, newer bool, err error) {
	rel, err := latestRelease()
	if err != nil {
		return "", false, err
	}
	return rel.TagName, compareSemver(current, rel.TagName) < 0, nil
}

// cacheState is persisted between runs so the startup check hits the network at
// most once per checkInterval.
type cacheState struct {
	CheckedAt int64  `json:"checked_at"`
	Latest    string `json:"latest"`
}

func cachePath() (string, error) {
	dir, err := paths.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "update-check.json"), nil
}

// CheckCached reports whether a newer release than current is available, using a
// cached result when the last network check was less than checkInterval ago. It
// is best-effort: any error (including a disabled check) yields ("", false).
func CheckCached(current string) (latest string, newer bool) {
	if os.Getenv(noCheckEnv) != "" {
		return "", false
	}
	path, err := cachePath()
	if err != nil {
		return "", false
	}

	var st cacheState
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &st)
	}

	fresh := st.CheckedAt > 0 && time.Since(time.Unix(st.CheckedAt, 0)) < checkInterval
	if !fresh {
		rel, err := latestRelease()
		if err != nil {
			return "", false
		}
		st = cacheState{CheckedAt: time.Now().Unix(), Latest: rel.TagName}
		if data, err := json.Marshal(st); err == nil {
			_ = os.WriteFile(path, data, 0o600)
		}
	}

	if st.Latest == "" {
		return "", false
	}
	return st.Latest, compareSemver(current, st.Latest) < 0
}

// SelfUpdate downloads the latest release for the current platform, verifies its
// SHA256 against the release's SHA256SUMS, and replaces the running binary. It
// returns the new version, or "" if already up to date.
func SelfUpdate(current string) (string, error) {
	rel, err := latestRelease()
	if err != nil {
		return "", err
	}
	if compareSemver(current, rel.TagName) >= 0 {
		return "", nil
	}

	want := assetName()
	var assetURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			assetURL = a.URL
		case "SHA256SUMS":
			sumsURL = a.URL
		}
	}
	if assetURL == "" {
		return "", fmt.Errorf("release %s has no asset %q", rel.TagName, want)
	}

	data, err := download(assetURL)
	if err != nil {
		return "", err
	}

	if sumsURL != "" {
		sums, err := download(sumsURL)
		if err != nil {
			return "", err
		}
		if err := verifySHA256(data, sums, want); err != nil {
			return "", err
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}

	// Write to a temp file in the same directory so the final rename is atomic
	// and stays on the same filesystem. This also surfaces permission problems
	// (e.g. a root-owned /usr/local/bin) before we touch the live binary.
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".tunneldir-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot write to %s: %w (try the install script or sudo)", filepath.Dir(exe), err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, exe); err != nil {
		return "", fmt.Errorf("cannot replace %s: %w (try the install script or sudo)", exe, err)
	}
	return rel.TagName, nil
}

func download(url string) ([]byte, error) {
	resp, err := client().Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s returned %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// verifySHA256 checks data against the checksum recorded for name in a
// sha256sum-format SHA256SUMS file ("<hex>  <name>").
func verifySHA256(data, sums []byte, name string) error {
	var want string
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum for %q in SHA256SUMS", name)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return nil
}

// compareSemver compares two "vX.Y.Z" version strings, tolerating a missing "v"
// prefix and missing trailing components. It returns -1 if a < b, 0 if equal,
// and 1 if a > b. Non-numeric or "dev" versions sort as oldest.
func compareSemver(a, b string) int {
	pa, pb := parseSemver(a), parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Drop any pre-release/build metadata.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{} // treat unparseable (e.g. "dev") as 0.0.0
		}
		out[i] = n
	}
	return out
}
