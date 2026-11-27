package socket

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyRestartScheduleReloadFailureCanRetry(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new schedule"
		if existing {
			name = "existing schedule"
		}
		t.Run(name, func(t *testing.T) {
			calls := stubRestartScheduleEnv(t)
			ctx := context.Background()
			if existing {
				if err := applyRestartSchedule(ctx, restartScheduleSetting{Enabled: true, Time: "04:00"}); err != nil {
					t.Fatal(err)
				}
			}
			oldCommand := tuneCommand
			tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
				if name == "systemctl" && strings.Join(args, " ") == "daemon-reload" {
					return "", errors.New("injected reload failure")
				}
				return oldCommand(ctx, name, args...)
			}
			next := restartScheduleSetting{Enabled: true, Time: "05:00"}
			if err := applyRestartSchedule(ctx, next); err == nil {
				t.Fatal("reload failure must fail apply")
			}
			// The retry can follow an agent restart, so the unfinished
			// reload must be remembered on disk rather than in memory.
			pendingPath := filepath.Join(restartScheduleUnitDir, "."+restartTimerUnitName+".reload-pending")
			if _, err := os.Stat(pendingPath); err != nil {
				t.Errorf("failed reload must leave a durable retry marker: %v", err)
			}
			tuneCommand = oldCommand
			*calls = nil
			if err := applyRestartSchedule(ctx, next); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			if n := countCall(*calls, "daemon-reload"); n != 1 {
				t.Fatalf("retry must reload the changed schedule before reporting applied, got %d reloads: %v", n, *calls)
			}
			if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
				t.Fatalf("successful reload must clear the retry marker: %v", err)
			}
		})
	}
}

func TestApplyRestartScheduleDisableAbsentClearsReloadMarker(t *testing.T) {
	stubRestartScheduleEnv(t)
	pendingPath := filepath.Join(restartScheduleUnitDir, "."+restartTimerUnitName+".reload-pending")
	if err := os.WriteFile(pendingPath, []byte("pending\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "show" {
			return "LoadState=not-found\nActiveState=inactive\n", nil
		}
		return "", errors.New("unit does not exist")
	}
	if err := applyRestartSchedule(context.Background(), restartScheduleSetting{Time: "04:00"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("disabled absent schedule left reload marker: %v", err)
	}
}

func TestApplyRestartScheduleDisableResumesPartialCleanup(t *testing.T) {
	stubRestartScheduleEnv(t)
	// A previous attempt removed the timer but could not remove its
	// companion service. The manager now truthfully reports no timer.
	servicePath := filepath.Join(restartScheduleUnitDir, restartServiceUnitName)
	if err := os.WriteFile(servicePath, []byte("leftover service"), 0o644); err != nil {
		t.Fatal(err)
	}
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "disable" {
			return "", errors.New("timer does not exist")
		}
		if len(args) > 0 && args[0] == "show" {
			return "ActiveState=inactive\nLoadState=not-found\n", nil
		}
		return "", nil
	}
	if err := applyRestartSchedule(context.Background(), restartScheduleSetting{Time: "04:00"}); err != nil {
		t.Fatalf("cannot resume partial disabled cleanup: %v", err)
	}
	if _, err := os.Stat(servicePath); !os.IsNotExist(err) {
		t.Fatalf("partial cleanup left service file: %v", err)
	}
}
