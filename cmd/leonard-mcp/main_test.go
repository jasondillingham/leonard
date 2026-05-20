package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestTranslateExitErr_SignalsAreCleanShutdown covers mcp F4: SIGINT/SIGTERM
// cancels the run-context, which surfaces as context.Canceled from the MCP
// SDK's srv.Run. main() then exits 1, making any process supervisor (Claude
// Code, systemd, docker) treat a graceful shutdown as a crash. The
// translation layer should map context.Canceled → nil so main() exits 0.
func TestTranslateExitErr_SignalsAreCleanShutdown(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   error
		want error // nil means "expect nil"
	}{
		{"nil passes through", nil, nil},
		{"context.Canceled becomes nil", context.Canceled, nil},
		{"wrapped Canceled becomes nil", fmt.Errorf("server stopped: %w", context.Canceled), nil},
		{"DeadlineExceeded is NOT swallowed", context.DeadlineExceeded, context.DeadlineExceeded},
		{"unrelated error is preserved", errors.New("disk on fire"), errors.New("disk on fire")},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := translateExitErr(tc.in)
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("translateExitErr(%v) = %v, want nil", tc.in, got)
			case tc.want != nil && got == nil:
				t.Errorf("translateExitErr(%v) = nil, want %v", tc.in, tc.want)
			case tc.want != nil && got != nil && got.Error() != tc.want.Error():
				t.Errorf("translateExitErr(%v) = %q, want %q", tc.in, got.Error(), tc.want.Error())
			}
		})
	}
}
