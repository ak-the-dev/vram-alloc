package main

import (
	"math"
	"strings"
	"testing"
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
		{name: "unknown total allows value", valueMB: 999999, totalMB: 0, shouldErr: false},
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
