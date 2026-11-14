// Package update asks GitHub whether a newer release of code-speed is out
// and, when asked, installs it in place (install.go).
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "daemonx666/code-speed"

// ReleasesURL is the page a user downloads from.
const ReleasesURL = "https://github.com/" + Repo + "/releases"

// API is GitHub's API root; tests point it elsewhere.
var API = "https://api.github.com"

// Release is the newest published release.
type Release struct {
	Version string  `json:"version"`
	URL     string  `json:"url"`
	Newer   bool    `json:"newer"` // newer than the running version
	Assets  []Asset `json:"-"`
}

// Latest asks for the newest release and compares it with current.
func Latest(ctx context.Context, current string) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", API+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "code-speed")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	var body struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, err
	}
	r := Release{Version: body.Tag, URL: body.URL}
	for _, a := range body.Assets {
		r.Assets = append(r.Assets, Asset{a.Name, a.URL, a.Size})
	}
	if !strings.HasPrefix(r.URL, "https://github.com/") {
		r.URL = ReleasesURL
	}
	r.Newer = Newer(body.Tag, current)
	return r, nil
}

// Newer reports whether version a is newer than b ("v1.2.3"); a build
// that isn't a release ("dev", "v0.4.0-dev") is older than any release of
// its numbers.
func Newer(a, b string) bool {
	pa, pre := parse(a)
	pb, preB := parse(b)
	if pa == nil {
		return false
	}
	if pb == nil {
		return true
	}
	for i := range 3 {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return !pre && preB
}

func parse(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	pre := false
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v, pre = v[:i], true
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil, false
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, pre
}
