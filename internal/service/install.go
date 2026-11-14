package service

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/daemonx666/code-speed/internal/update"
)

// UpdateState is where installing an update stands, for Settings.
type UpdateState struct {
	// Plan is how this copy would update; CanInstall false with Reason
	// when it can't (the release page has the download)
	CanInstall bool        `json:"canInstall"`
	Reason     string      `json:"reason,omitempty"`
	Plan       update.Plan `json:"plan"`
	Step       string      `json:"step,omitempty"` // downloading, installing
	Error      string      `json:"error,omitempty"`
}

// ErrNoRestart: this build (the terminal one, the dev page) can't restart
// itself into a new copy.
var ErrNoRestart = errors.New("only the desktop app updates itself")

func (s *Service) plan(version string) (update.Plan, error) {
	if s.HelperPath == "" || s.Restart == nil {
		return update.Plan{}, ErrNoRestart
	}
	return update.PlanFor(runtime.GOOS, runtime.GOARCH, s.HelperPath, version)
}

// UpdateState says whether the newest release can be installed here.
func (s *Service) UpdateState() UpdateState {
	s.mu.Lock()
	latest, step, ierr := s.latest, s.installStep, s.installErr
	s.mu.Unlock()
	st := UpdateState{Step: step, Error: ierr}
	if latest == nil || !latest.Newer {
		return st
	}
	p, err := s.plan(latest.Version)
	st.Plan = p
	if err != nil {
		st.Reason = err.Error()
	} else {
		st.CanInstall = true
	}
	return st
}

// InstallUpdate downloads the newest release, checks it against the
// release's checksums, puts it in place and restarts into it.
func (s *Service) InstallUpdate(ctx context.Context) error {
	s.mu.Lock()
	latest := s.latest
	if s.installStep != "" {
		s.mu.Unlock()
		return errors.New("already installing")
	}
	if latest == nil || !latest.Newer {
		s.mu.Unlock()
		return errors.New("no newer release")
	}
	s.installStep, s.installErr = "downloading", ""
	s.mu.Unlock()
	fail := func(err error) error {
		s.mu.Lock()
		s.installStep, s.installErr = "", err.Error()
		s.mu.Unlock()
		return err
	}
	p, err := s.plan(latest.Version)
	if err != nil {
		return fail(err)
	}
	staged, err := update.Download(ctx, p, latest.Assets)
	if err != nil {
		return fail(err)
	}
	s.mu.Lock()
	s.installStep = "installing"
	s.mu.Unlock()
	cmd, err := staged.Apply(os.Getpid())
	if err != nil {
		os.RemoveAll(staged.Dir)
		return fail(err)
	}
	s.Restart(cmd)
	return nil
}

// autoInstall installs a newer release found by the update check, when
// the user asked for that.
func (s *Service) autoInstall(ctx context.Context) {
	if st := s.UpdateState(); s.Settings().AutoUpdate && st.CanInstall && st.Step == "" && st.Error == "" {
		_ = s.InstallUpdate(ctx)
	}
}
