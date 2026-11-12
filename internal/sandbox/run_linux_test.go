//go:build linux

package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/arwos/isobox/internal/preflight"
	"github.com/arwos/isobox/internal/profile"
)

func TestShouldSignalProcessGroup(t *testing.T) {
	tests := []struct {
		name            string
		groupID         int
		supervisorGroup int
		want            bool
	}{
		{name: "missing group", groupID: 0, supervisorGroup: 42, want: false},
		{name: "same group as supervisor", groupID: 42, supervisorGroup: 42, want: false},
		{name: "separate child group", groupID: 84, supervisorGroup: 42, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldSignalProcessGroup(test.groupID, test.supervisorGroup); got != test.want {
				t.Fatalf("shouldSignalProcessGroup(%d, %d) = %t, want %t", test.groupID, test.supervisorGroup, got, test.want)
			}
		})
	}
}

type fakeNetworkProcess struct {
	readyErr     error
	configureErr error
	stopped      bool
	configured   bool
	done         chan struct{}
}

func (process *fakeNetworkProcess) waitReady() error { return process.readyErr }

func (process *fakeNetworkProcess) configureForwards([]profile.PortRule) error {
	process.configured = true
	return process.configureErr
}

func (process *fakeNetworkProcess) stop() { process.stopped = true }

func (process *fakeNetworkProcess) doneChannel() <-chan struct{} { return process.done }

func (process *fakeNetworkProcess) error() error { return nil }

func TestSetupNetworkBackendFallsBackBeforeGateRelease(t *testing.T) {
	pasta := &fakeNetworkProcess{readyErr: errors.New("pasta namespace setup denied")}
	slirp := &fakeNetworkProcess{done: make(chan struct{})}
	network := NetworkRuntime{
		Backend: preflight.BackendPasta,
		Fallback: &NetworkRuntime{
			Backend:    preflight.BackendSlirp4netns,
			Executable: "/usr/bin/slirp4netns",
		},
	}
	var diagnostics bytes.Buffer
	var calls []preflight.BackendKind
	start := func(runtime NetworkRuntime, _ int, _ []profile.PortRule, _, _ io.Writer) (managedNetworkProcess, error) {
		calls = append(calls, runtime.activeBackend())
		switch runtime.activeBackend() {
		case preflight.BackendPasta:
			return pasta, nil
		case preflight.BackendSlirp4netns:
			return slirp, nil
		default:
			return nil, fmt.Errorf("unexpected backend %q", runtime.activeBackend())
		}
	}

	got, err := setupNetworkBackendWithFallback(&network, 123, nil, nil, &diagnostics, start)
	if err != nil {
		t.Fatalf("setupNetworkBackendWithFallback() error = %v", err)
	}
	if got != slirp || !pasta.stopped || !slirp.configured {
		t.Fatalf("fallback state = (process=%p pastaStopped=%t slirpConfigured=%t), want (%p, true, true)",
			got, pasta.stopped, slirp.configured, slirp)
	}
	if network.activeBackend() != preflight.BackendSlirp4netns || network.Fallback != nil {
		t.Fatalf("network after fallback = (backend=%q fallback=%#v), want slirp4netns without another fallback", network.activeBackend(), network.Fallback)
	}
	if strings.Join([]string{string(calls[0]), string(calls[1])}, ",") != "pasta,slirp4netns" {
		t.Fatalf("backend start order = %v, want pasta then slirp4netns", calls)
	}
	if !strings.Contains(diagnostics.String(), "trying compatible slirp4netns fallback") {
		t.Fatalf("fallback diagnostic = %q, want an explicit fallback message", diagnostics.String())
	}
}

func TestSetupNetworkBackendDoesNotFallbackWithoutValidatedAlternative(t *testing.T) {
	pasta := &fakeNetworkProcess{readyErr: errors.New("pasta namespace setup denied")}
	network := NetworkRuntime{Backend: preflight.BackendPasta}
	calls := 0
	start := func(NetworkRuntime, int, []profile.PortRule, io.Writer, io.Writer) (managedNetworkProcess, error) {
		calls++
		return pasta, nil
	}
	_, err := setupNetworkBackendWithFallback(&network, 123, nil, nil, nil, start)
	if err == nil || !strings.Contains(err.Error(), "pasta setup failed: pasta namespace setup denied") {
		t.Fatalf("setupNetworkBackendWithFallback() error = %v, want the pasta setup error", err)
	}
	if calls != 1 || !pasta.stopped {
		t.Fatalf("starter calls=%d pastaStopped=%t, want one call and cleanup", calls, pasta.stopped)
	}
}

func TestSetupNetworkBackendFailsClosedWhenFallbackAlsoFails(t *testing.T) {
	pasta := &fakeNetworkProcess{readyErr: errors.New("pasta namespace setup denied")}
	slirp := &fakeNetworkProcess{configureErr: errors.New("slirp host-forward API rejected the port")}
	network := NetworkRuntime{
		Backend: preflight.BackendPasta,
		Fallback: &NetworkRuntime{
			Backend:    preflight.BackendSlirp4netns,
			Executable: "/usr/bin/slirp4netns",
		},
	}
	start := func(runtime NetworkRuntime, _ int, _ []profile.PortRule, _, _ io.Writer) (managedNetworkProcess, error) {
		if runtime.activeBackend() == preflight.BackendPasta {
			return pasta, nil
		}
		return slirp, nil
	}
	_, err := setupNetworkBackendWithFallback(&network, 123, nil, nil, nil, start)
	if err == nil || !strings.Contains(err.Error(), "compatible slirp4netns fallback failed: slirp host-forward API rejected the port") {
		t.Fatalf("setupNetworkBackendWithFallback() error = %v, want both startup failures", err)
	}
	if !pasta.stopped || !slirp.stopped || network.activeBackend() != preflight.BackendPasta {
		t.Fatalf("cleanup state = (pastaStopped=%t slirpStopped=%t backend=%q), want both stopped and original backend retained",
			pasta.stopped, slirp.stopped, network.activeBackend())
	}
}
