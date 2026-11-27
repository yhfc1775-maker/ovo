package socket

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyRestartScheduleDisableFailurePreservesUnits(t *testing.T) {
	calls := stubRestartScheduleEnv(t)
	ctx := context.Background()
	if err := applyRestartSchedule(ctx, restartScheduleSetting{Enabled: true, Time: "04:00"}); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(restartScheduleUnitDir, restartTimerUnitName), filepath.Join(restartScheduleUnitDir, restartServiceUnitName)}
	before := make([][]byte, len(paths))
	for i, p := range paths {
		var err error
		before[i], err = os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	*calls = nil
	oldCommand := tuneCommand
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "systemctl" && strings.Join(args, " ") == "disable --now "+restartTimerUnitName {
			return "", errors.New("injected stop failure")
		}
		return oldCommand(ctx, name, args...)
	}
	err := applyRestartSchedule(ctx, restartScheduleSetting{Enabled: false, Time: "04:00"})
	if err == nil || !strings.Contains(err.Error(), "injected stop failure") {
		t.Errorf("disable failure must be reported, got %v", err)
	}
	for i, p := range paths {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != string(before[i]) {
			t.Errorf("failed disable must retain unit %s for retry: contents=%q err=%v", p, got, err)
		}
	}
	if countCall(*calls, "daemon-reload") != 0 {
		t.Fatalf("failed stop must not reload a deleted schedule: %v", *calls)
	}
}

func TestApplyRestartScheduleDisableAbsentChecksManagerState(t *testing.T) {
	for _, tt := range []struct {
		name    string
		state   string
		wantErr bool
	}{
		{"never installed", "LoadState=not-found\nActiveState=inactive\n", false},
		{"still active without files", "LoadState=not-found\nActiveState=active\n", true},
		{"manager unavailable", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stubRestartScheduleEnv(t)
			tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "show" {
					// systemctl may report a nonzero exit code for a missing
					// unit even when it returns the requested properties.
					return tt.state, errors.New("unit absent or manager unavailable")
				}
				return "", errors.New("injected disable failure")
			}
			err := applyRestartSchedule(context.Background(), restartScheduleSetting{Time: "04:00"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("disable absent schedule: err=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
