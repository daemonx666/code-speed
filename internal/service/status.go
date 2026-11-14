package service

import (
	"context"
	"strings"
	"sync"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/status"
	"github.com/daemonx666/code-speed/internal/usage"
)

var (
	statusOnce    sync.Once
	statusChecker *status.Checker
)

func (s *Service) statuses() *status.Checker {
	statusOnce.Do(func() { statusChecker = status.NewChecker() })
	return statusChecker
}

// StatusIDs are the providers the overview lists: the user's pick, or
// those of the agents that are on and installed here.
func (s *Service) StatusIDs() []string {
	st := s.Settings()
	if st.StatusCustom {
		return st.StatusProviders
	}
	var srcs []usage.Source
	for _, sp := range source.Specs() {
		if st.Enabled(sp.Source) && sp.Detected() {
			srcs = append(srcs, sp.Source)
		}
	}
	return status.For(srcs)
}

// ProviderStatus is one provider as the panel shows it.
type ProviderStatus struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Page        string            `json:"page"`
	Feed        bool              `json:"feed"` // it has a status feed (not a link only)
	Indicator   string            `json:"indicator"`
	Description string            `json:"description,omitempty"`
	CheckedMs   int64             `json:"checkedMs,omitempty"`
	Today       []status.Incident `json:"today"`
	Error       string            `json:"error,omitempty"`
	Fails       int               `json:"fails,omitempty"`
	NextMs      int64             `json:"nextMs,omitempty"`
}

// Status lists the providers to show with their last state.
func (s *Service) Status() []ProviderStatus {
	ids := s.StatusIDs()
	states := s.statuses().States(ids)
	out := make([]ProviderStatus, 0, len(ids))
	for i, id := range ids {
		p, _ := status.Lookup(id)
		st := states[i]
		out = append(out, ProviderStatus{ID: id, Name: p.Name, Page: p.Page, Feed: p.API != "",
			Indicator: st.Indicator, Description: st.Description, CheckedMs: st.CheckedMs, Today: st.Today,
			Error: st.Error, Fails: st.Fails, NextMs: st.NextMs})
	}
	return out
}

// CheckStatus reads the shown providers' feeds, when the user allowed it;
// force (the user asked) also tries the ones backing off after failures.
func (s *Service) CheckStatus(ctx context.Context, force bool) {
	if !s.Settings().StatusCheck {
		return
	}
	s.statuses().Check(ctx, s.StatusIDs(), force)
}

// IncidentLink is a link of one of the shown providers' incidents today,
// so the panel opens only what the feeds gave.
func (s *Service) IncidentLink(u string) bool {
	if !strings.HasPrefix(u, "https://") {
		return false
	}
	for _, p := range s.Status() {
		for _, in := range p.Today {
			if in.Shortlink == u {
				return true
			}
		}
	}
	return false
}
