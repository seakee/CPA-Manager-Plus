package main

import (
	"context"
	"testing"
	"time"
)

type recordingInspectionStopper struct {
	calls       int
	firstErr    error
	hasDeadline bool
}

func (s *recordingInspectionStopper) StopAndWait(ctx context.Context) error {
	s.calls++
	_, s.hasDeadline = ctx.Deadline()
	return s.firstErr
}

func TestNewPprofServer(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantNil bool
		wantErr bool
	}{
		{name: "disabled", wantNil: true},
		{name: "ipv4 loopback", addr: "127.0.0.1:6060"},
		{name: "ipv6 loopback", addr: "[::1]:6060"},
		{name: "localhost", addr: "localhost:6060"},
		{name: "all interfaces", addr: ":6060", wantErr: true},
		{name: "public address", addr: "0.0.0.0:6060", wantErr: true},
		{name: "invalid", addr: "localhost", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := newPprofServer(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("newPprofServer(%q) error = %v", tt.addr, err)
			}
			if tt.wantNil && server != nil {
				t.Fatalf("newPprofServer(%q) = %#v, want nil", tt.addr, server)
			}
			if !tt.wantNil && !tt.wantErr && server == nil {
				t.Fatalf("newPprofServer(%q) = nil", tt.addr)
			}
		})
	}
}

func TestStopCodexInspectionWorkerRemainsBoundedAfterTimeout(t *testing.T) {
	stopper := &recordingInspectionStopper{firstErr: context.DeadlineExceeded}
	stopCodexInspectionWorker(stopper, time.Millisecond)
	if stopper.calls != 1 || !stopper.hasDeadline {
		t.Fatalf("stop calls = %d hasDeadline=%v, want one bounded stop", stopper.calls, stopper.hasDeadline)
	}
}

func TestStopCodexInspectionWorkerDoesNotDrainAfterCleanStop(t *testing.T) {
	stopper := &recordingInspectionStopper{}
	stopCodexInspectionWorker(stopper, time.Millisecond)
	if stopper.calls != 1 {
		t.Fatalf("stop calls = %d, want 1", stopper.calls)
	}
}
