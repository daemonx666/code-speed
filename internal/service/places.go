package service

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/appdir"
	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/settings"
	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// extraOf is where else the settings say to look for logs.
func extraOf(st settings.Settings) source.Extra {
	x := source.Extra{Homes: slices.Clone(st.Homes), Paths: st.Paths}
	if st.WSL {
		x.Homes = append(x.Homes, source.WSLHomes()...)
	}
	return x
}

func placesChanged(a, b settings.Settings) bool {
	if a.WSL != b.WSL || !slices.Equal(a.Homes, b.Homes) || len(a.Paths) != len(b.Paths) {
		return true
	}
	for k, v := range a.Paths {
		if !slices.Equal(v, b.Paths[k]) {
			return true
		}
	}
	return false
}

// movedSources lists the agents whose log folders differ between two
// sets of places: their history is rebuilt, since a new folder's old
// days would otherwise be skipped and a dropped one's would stay counted.
func movedSources(st settings.Settings, before, after source.Extra) []usage.Source {
	on := func(src usage.Source) bool { return st.Enabled(src) }
	group := func(rs []source.Root) map[usage.Source][]string {
		m := map[usage.Source][]string{}
		for _, r := range rs {
			m[r.Source] = append(m[r.Source], r.Dir)
		}
		for k := range m {
			slices.Sort(m[k])
		}
		return m
	}
	a, b := group(source.RootsWith(on, before)), group(source.RootsWith(on, after))
	var out []usage.Source
	for _, spec := range source.Specs() {
		if !slices.Equal(a[spec.Source], b[spec.Source]) {
			out = append(out, spec.Source)
		}
	}
	return out
}

// RootsOf lists an agent's log folders now in use, for Settings.
func (s *Service) RootsOf() map[usage.Source][]string {
	st := s.Settings()
	m := map[usage.Source][]string{}
	for _, r := range source.RootsWith(func(src usage.Source) bool { return st.Enabled(src) }, extraOf(st)) {
		m[r.Source] = append(m[r.Source], r.Dir)
	}
	return m
}

// synced is the downloaded price table's folder.
func synced() pricing.Synced { return pricing.Synced{Dir: filepath.Join(appdir.Dir(), "prices")} }

// PriceSync is the state of the price table: the version in use and what
// the last syncs did.
func (s *Service) PriceSync() (int, string, pricing.SyncState) {
	t := synced().Current(time.Now())
	return t.Version, t.Updated, synced().State()
}

var syncClient = &http.Client{
	Timeout: 20 * time.Second,
	// redirects only to https on the table's own hosts
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		h := req.URL.Hostname()
		if len(via) > 3 || req.URL.Scheme != "https" ||
			!(h == "raw.githubusercontent.com" || h == "cdn.jsdelivr.net" || strings.HasSuffix(h, ".githubusercontent.com")) {
			return errors.New("redirect refused")
		}
		return nil
	},
}

// SyncPrices fetches a newer price table when the user allowed it (or
// force: the Check now button) and, when one came, reprices with it.
func (s *Service) SyncPrices(ctx context.Context, force bool) (bool, error) {
	if !force && !s.Settings().PriceSync {
		return false, nil
	}
	changed, err := synced().Sync(ctx, syncClient, time.Now())
	if changed {
		s.Reload()
	}
	return changed, err
}

// PriceSyncDue reports whether a daily sync is due: a day since the last
// success, or an hour since a failed attempt.
func (s *Service) PriceSyncDue(now time.Time) bool {
	st := synced().State()
	if st.LastAttemptMs > st.LastSuccessMs && now.UnixMilli()-st.LastAttemptMs < int64(time.Hour/time.Millisecond) {
		return false
	}
	return now.UnixMilli()-st.LastSuccessMs >= int64(24*time.Hour/time.Millisecond)
}
