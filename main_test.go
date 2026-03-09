package main

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v3/mem"
)

func TestParseCustomVRAMInput(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{name: "valid integer", input: "2048", want: 2048},
		{name: "trimmed zero", input: " 0 ", want: 0},
		{name: "empty", input: "", wantErr: true},
		{name: "negative", input: "-1", wantErr: true},
		{name: "float", input: "2.5", wantErr: true},
		{name: "text", input: "abc", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCustomVRAMInput(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got value %d", got)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, got)
			}
		})
	}
}

func TestBuildVRAMPresets(t *testing.T) {
	totalMemMB := 32768 // 32 GB
	presets := buildVRAMPresets(totalMemMB)
	if len(presets) == 0 {
		t.Fatal("expected at least one preset")
	}

	maxMB := getMaxVRAMLimitMB(totalMemMB)
	seen := map[int]struct{}{}
	prev := 0

	for _, preset := range presets {
		if preset.MB < minPresetMB {
			t.Fatalf("preset below minimum: %d", preset.MB)
		}
		if preset.MB > maxMB {
			t.Fatalf("preset above max: %d > %d", preset.MB, maxMB)
		}
		if preset.MB <= prev {
			t.Fatalf("presets are not strictly increasing: %d after %d", preset.MB, prev)
		}
		if _, exists := seen[preset.MB]; exists {
			t.Fatalf("duplicate preset detected: %d", preset.MB)
		}
		if !strings.Contains(preset.Label, "%") {
			t.Fatalf("preset label missing percent: %q", preset.Label)
		}

		seen[preset.MB] = struct{}{}
		prev = preset.MB
	}
}

func TestBuildVRAMPresetsLowMemory(t *testing.T) {
	presets := buildVRAMPresets(900)
	if len(presets) != 0 {
		t.Fatalf("expected no presets, got %d", len(presets))
	}
}

func TestGetMaxVRAMLimitMB(t *testing.T) {
	tests := []struct {
		name   string
		total  int
		expect int
	}{
		{name: "no memory", total: 0, expect: 0},
		{name: "negative memory", total: -1, expect: 0},
		{name: "32GB", total: 32768, expect: int(math.Floor(32768 * 0.90))},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := getMaxVRAMLimitMB(tc.total)
			if got != tc.expect {
				t.Fatalf("expected %d, got %d", tc.expect, got)
			}
		})
	}
}

func TestValidateVRAMLimitMB(t *testing.T) {
	max := getMaxVRAMLimitMB(32768)

	tests := []struct {
		name      string
		valueMB   int
		totalMB   int
		shouldErr bool
	}{
		{name: "dynamic", valueMB: 0, totalMB: 32768, shouldErr: false},
		{name: "valid value", valueMB: 8192, totalMB: 32768, shouldErr: false},
		{name: "negative", valueMB: -1, totalMB: 32768, shouldErr: true},
		{name: "above max", valueMB: max + 1, totalMB: 32768, shouldErr: true},
		{name: "unknown total rejects positive value", valueMB: 999999, totalMB: 0, shouldErr: true},
		{name: "unknown total still allows dynamic", valueMB: 0, totalMB: 0, shouldErr: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validateVRAMLimitMB(tc.valueMB, tc.totalMB)
			if tc.shouldErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.shouldErr && err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		})
	}
}

func TestSysctlReadSpec(t *testing.T) {
	spec := sysctlReadSpec("-n", vramSysctlKey)
	if spec.path != sysctlPath {
		t.Fatalf("expected path %q, got %q", sysctlPath, spec.path)
	}
	if spec.label != "sysctl" {
		t.Fatalf("expected label sysctl, got %q", spec.label)
	}
	if spec.timeout != readCommandTimeout {
		t.Fatalf("expected timeout %v, got %v", readCommandTimeout, spec.timeout)
	}
	if len(spec.args) != 2 || spec.args[0] != "-n" || spec.args[1] != vramSysctlKey {
		t.Fatalf("unexpected args: %#v", spec.args)
	}
}

func TestInteractiveAppleScriptSpec(t *testing.T) {
	spec := interactiveAppleScriptSpec(`display notification "hello"`)
	if spec.path != osascriptPath {
		t.Fatalf("expected path %q, got %q", osascriptPath, spec.path)
	}
	if spec.label != "osascript" {
		t.Fatalf("expected label osascript, got %q", spec.label)
	}
	if spec.timeout != 0 {
		t.Fatalf("expected no timeout, got %v", spec.timeout)
	}
	if len(spec.args) != 2 || spec.args[0] != "-e" {
		t.Fatalf("unexpected args: %#v", spec.args)
	}
}

func TestNotificationAppleScriptSpec(t *testing.T) {
	spec := notificationAppleScriptSpec(`display notification "hello"`)
	if spec.path != osascriptPath {
		t.Fatalf("expected path %q, got %q", osascriptPath, spec.path)
	}
	if spec.timeout != notifyCommandTimeout {
		t.Fatalf("expected timeout %v, got %v", notifyCommandTimeout, spec.timeout)
	}
}

func TestRefreshStatusMessage(t *testing.T) {
	if got := refreshStatusMessage(nil); got != "Manual refresh completed" {
		t.Fatalf("unexpected success message: %q", got)
	}

	err := errors.New("refresh VRAM limit: permission denied")
	got := refreshStatusMessage(err)
	if !strings.Contains(got, "Manual refresh failed:") {
		t.Fatalf("expected failure prefix, got %q", got)
	}
	if !strings.Contains(got, err.Error()) {
		t.Fatalf("expected wrapped error in message, got %q", got)
	}
}

func TestCommandSpecsUseExpectedTimeoutPolicy(t *testing.T) {
	if sysctlReadSpec("-n", "machdep.cpu.brand_string").timeout <= 0 {
		t.Fatal("sysctl reads should use a timeout")
	}
	if interactiveAppleScriptSpec(`display dialog "hi"`).timeout != 0 {
		t.Fatal("interactive AppleScript should not use a timeout")
	}
	if notificationAppleScriptSpec(`display notification "hi"`).timeout <= 0 {
		t.Fatal("notifications should use a timeout")
	}
	if readCommandTimeout != 5*time.Second {
		t.Fatalf("unexpected read timeout: %v", readCommandTimeout)
	}
}

func TestSuccessMessageForVRAMLimit(t *testing.T) {
	if got := successMessageForVRAMLimit(defaultVRAMLimitMB); got != "Set to Dynamic (0 MB)" {
		t.Fatalf("unexpected default success message: %q", got)
	}

	got := successMessageForVRAMLimit(4096)
	if !strings.Contains(got, "Set to 4096 MB") {
		t.Fatalf("unexpected custom success message: %q", got)
	}
}

func TestCollectRefreshSnapshot(t *testing.T) {
	origMemoryReader := virtualMemoryReader
	origVRAMReader := currentVRAMLimitReader
	t.Cleanup(func() {
		virtualMemoryReader = origMemoryReader
		currentVRAMLimitReader = origVRAMReader
	})

	virtualMemoryReader = func() (*mem.VirtualMemoryStat, error) {
		return &mem.VirtualMemoryStat{
			Used:        8 * 1024 * 1024 * 1024,
			Total:       16 * 1024 * 1024 * 1024,
			UsedPercent: 50,
		}, nil
	}
	currentVRAMLimitReader = func() (int, error) {
		return 4096, nil
	}

	snapshot, err := collectRefreshSnapshot(true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !snapshot.hasRAM {
		t.Fatal("expected RAM snapshot to be present")
	}
	if snapshot.vramLimitMB != 4096 {
		t.Fatalf("expected vram limit 4096, got %d", snapshot.vramLimitMB)
	}
	if snapshot.usedPercent != 50 {
		t.Fatalf("expected used percent 50, got %v", snapshot.usedPercent)
	}
}

func TestCollectRefreshSnapshotMemoryError(t *testing.T) {
	origMemoryReader := virtualMemoryReader
	t.Cleanup(func() {
		virtualMemoryReader = origMemoryReader
	})

	virtualMemoryReader = func() (*mem.VirtualMemoryStat, error) {
		return nil, errors.New("mem unavailable")
	}

	snapshot, err := collectRefreshSnapshot(false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if snapshot.hasRAM {
		t.Fatal("did not expect RAM snapshot on memory error")
	}
	if !strings.Contains(err.Error(), "refresh RAM stats") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCollectRefreshSnapshotVRAMError(t *testing.T) {
	origMemoryReader := virtualMemoryReader
	origVRAMReader := currentVRAMLimitReader
	t.Cleanup(func() {
		virtualMemoryReader = origMemoryReader
		currentVRAMLimitReader = origVRAMReader
	})

	virtualMemoryReader = func() (*mem.VirtualMemoryStat, error) {
		return &mem.VirtualMemoryStat{
			Used:        4 * 1024 * 1024 * 1024,
			Total:       16 * 1024 * 1024 * 1024,
			UsedPercent: 25,
		}, nil
	}
	currentVRAMLimitReader = func() (int, error) {
		return 0, errors.New("permission denied")
	}

	snapshot, err := collectRefreshSnapshot(true)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !snapshot.hasRAM {
		t.Fatal("expected RAM snapshot even when VRAM read fails")
	}
	if !strings.Contains(err.Error(), "refresh VRAM limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}
