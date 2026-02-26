package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"
	"github.com/shirou/gopsutil/v3/mem"
)

const (
	updateInterval = 3 * time.Second
	commandTimeout = 5 * time.Second
	vramSysctlKey  = "iogpu.wired_limit_mb"

	minPresetPercent = 5
	maxPresetPercent = 90
	presetStep       = 5
	minPresetMB      = 1024
)

var errUserCanceled = errors.New("user canceled")

type vramPreset struct {
	Label   string
	MB      int
	Percent int
}

var (
	isAppleSilicon bool
	ramDetailItem  *systray.MenuItem
	vramDetailItem *systray.MenuItem
	lastActionItem *systray.MenuItem

	totalMemoryMB  int
	maxVRAMLimitMB int
	updateMu       sync.Mutex
)

func main() {
	checkAppleSilicon()
	systray.Run(onReady, onExit)
}

func checkAppleSilicon() {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return
	}

	// Default to true for darwin/arm64, then verify via CPU brand string when available.
	isAppleSilicon = true

	out, err := commandOutput("sysctl", "-n", "machdep.cpu.brand_string")
	if err != nil {
		return
	}
	isAppleSilicon = strings.Contains(string(out), "Apple")
}

func onReady() {
	totalMemoryMB = getTotalMemoryMB()
	maxVRAMLimitMB = getMaxVRAMLimitMB(totalMemoryMB)

	systray.SetTitle("RAM: --%")
	systray.SetTooltip("Memory & VRAM Monitor")

	ramDetailItem = systray.AddMenuItem("Loading RAM stats...", "Memory Statistics")
	ramDetailItem.Disable()

	systray.AddSeparator()

	if isAppleSilicon {
		if totalMemoryMB > 0 {
			item := systray.AddMenuItem(
				fmt.Sprintf("Total Memory: %d MB (%s)", totalMemoryMB, formatGBFromMB(totalMemoryMB)),
				"Detected physical memory",
			)
			item.Disable()
		}

		if maxVRAMLimitMB > 0 {
			item := systray.AddMenuItem(
				fmt.Sprintf("Max VRAM Limit: %d MB (%s)", maxVRAMLimitMB, formatGBFromMB(maxVRAMLimitMB)),
				"App safety cap at 90% of total memory",
			)
			item.Disable()
		}

		systray.AddSeparator()
		vramDetailItem = systray.AddMenuItem("VRAM: Detecting...", "Current GPU memory limit")
		vramDetailItem.Disable()

		menuHelp := "Set iogpu.wired_limit_mb (admin required)"
		if maxVRAMLimitMB > 0 {
			menuHelp = fmt.Sprintf("Set 0..%d MB (admin required)", maxVRAMLimitMB)
		}
		vramMenu := systray.AddMenuItem("Set VRAM Limit", menuHelp)

		// Dynamic (Auto)
		dynamic := vramMenu.AddSubMenuItem("Dynamic (0 MB)", "Let macOS manage automatically")
		go watchVRAMSelection(dynamic, 0)

		// Generate dynamic presets
		totalMemMB := totalMemoryMB
		if totalMemMB == 0 {
			totalMemMB = getTotalMemoryMB()
		}
		for _, preset := range buildVRAMPresets(totalMemMB) {
			item := vramMenu.AddSubMenuItem(preset.Label, fmt.Sprintf("Set %d MB", preset.MB))
			go watchVRAMSelection(item, preset.MB)
		}

		custom := vramMenu.AddSubMenuItem("Custom...", "Enter specific MB value")
		go func() {
			for range custom.ClickedCh {
				mb, err := promptCustomVRAM()
				if err != nil {
					if errors.Is(err, errUserCanceled) {
						setActionStatus("Custom prompt canceled")
						continue
					}
					setActionStatus(fmt.Sprintf("Invalid custom value: %v", err))
					continue
				}
				applyVRAMLimit(mb)
			}
		}()
	} else {
		vramDetailItem = systray.AddMenuItem("VRAM control: Apple Silicon only", "Requires macOS arm64")
		vramDetailItem.Disable()
	}

	systray.AddSeparator()
	lastActionItem = systray.AddMenuItem("Last Action: none", "Latest VRAM command result")
	lastActionItem.Disable()

	systray.AddSeparator()
	refreshNow := systray.AddMenuItem("Refresh Now", "Refresh RAM and VRAM status immediately")
	go func() {
		for range refreshNow.ClickedCh {
			update()
			setActionStatus("Manual refresh completed")
		}
	}()

	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Exit")
	go func() {
		<-quit.ClickedCh
		systray.Quit()
	}()

	go monitor()
}

func watchVRAMSelection(item *systray.MenuItem, mb int) {
	for range item.ClickedCh {
		applyVRAMLimit(mb)
	}
}

func monitor() {
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()
	update()
	for range ticker.C {
		update()
	}
}

func update() {
	updateMu.Lock()
	defer updateMu.Unlock()

	v, err := mem.VirtualMemory()
	if err != nil {
		systray.SetTitle("ERR")
		return
	}

	systray.SetTitle(fmt.Sprintf("RAM: %.0f%%", math.Round(v.UsedPercent)))

	used := float64(v.Used) / 1024 / 1024 / 1024
	total := float64(v.Total) / 1024 / 1024 / 1024

	ramDetailItem.SetTitle(
		fmt.Sprintf("Used: %.1f / %.1f GB (%.0f%%)", used, total, v.UsedPercent),
	)

	if isAppleSilicon {
		updateVRAMStatus()
	}
}

func updateVRAMStatus() {
	if vramDetailItem == nil {
		return
	}

	current, err := getCurrentVRAMLimitMB()
	if err != nil {
		vramDetailItem.SetTitle("VRAM Limit: unavailable (check permissions)")
		return
	}

	if current == 0 {
		vramDetailItem.SetTitle("VRAM Limit: Dynamic (0 MB)")
		return
	}

	percentLabel := ""
	if totalMemoryMB > 0 {
		percentOfTotal := 100 * float64(current) / float64(totalMemoryMB)
		percentLabel = fmt.Sprintf(", %.0f%% of total", math.Round(percentOfTotal))
	}

	vramDetailItem.SetTitle(fmt.Sprintf(
		"VRAM Limit: %d MB (%s%s)",
		current,
		formatGBFromMB(current),
		percentLabel,
	))
}

func getTotalMemoryMB() int {
	v, err := mem.VirtualMemory()
	if err != nil {
		return 0
	}
	return int(v.Total / 1024 / 1024)
}

func buildVRAMPresets(totalMemMB int) []vramPreset {
	if totalMemMB <= 0 {
		return nil
	}

	maxPresetMB := getMaxVRAMLimitMB(totalMemMB)
	if maxPresetMB < minPresetMB {
		return nil
	}

	presets := make([]vramPreset, 0, (maxPresetPercent-minPresetPercent)/presetStep+1)
	seen := map[int]struct{}{}

	for percent := minPresetPercent; percent <= maxPresetPercent; percent += presetStep {
		mb := int(math.Round(float64(totalMemMB) * float64(percent) / 100))
		if mb < minPresetMB || mb > maxPresetMB {
			continue
		}
		if _, exists := seen[mb]; exists {
			continue
		}

		seen[mb] = struct{}{}
		presets = append(presets, vramPreset{
			Label:   fmt.Sprintf("%s (%d%%)", formatGBFromMB(mb), percent),
			MB:      mb,
			Percent: percent,
		})
	}

	return presets
}

func formatGBFromMB(mb int) string {
	gb := float64(mb) / 1024
	if math.Abs(gb-math.Round(gb)) < 0.01 {
		return fmt.Sprintf("%.0f GB", gb)
	}
	return fmt.Sprintf("%.1f GB", gb)
}

func applyVRAMLimit(mb int) {
	if !isAppleSilicon {
		message := "VRAM control unavailable on this machine"
		setActionStatus(message)
		notifyVRAMResult(false, message)
		return
	}

	if err := validateVRAMLimitMB(mb, totalMemoryMB); err != nil {
		message := fmt.Sprintf("Rejected: %v", err)
		setActionStatus(message)
		notifyVRAMResult(false, message)
		return
	}

	if err := setVRAMLimitMB(mb); err != nil {
		message := fmt.Sprintf("Failed: %v", err)
		setActionStatus(message)
		notifyVRAMResult(false, message)
		return
	}

	message := ""
	if mb == 0 {
		message = "Set to Dynamic (0 MB)"
	} else {
		message = fmt.Sprintf("Set to %d MB (%s)", mb, formatGBFromMB(mb))
	}
	setActionStatus(message)
	notifyVRAMResult(true, message)

	updateVRAMStatus()
}

func setActionStatus(message string) {
	if lastActionItem == nil {
		return
	}

	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "unknown"
	}

	if len(msg) > 72 {
		msg = msg[:72] + "..."
	}

	lastActionItem.SetTitle(
		fmt.Sprintf("Last Action (%s): %s", time.Now().Format("15:04:05"), msg),
	)
}

func setVRAMLimitMB(mb int) error {
	if mb < 0 {
		return errors.New("value must be >= 0")
	}

	command := fmt.Sprintf("/usr/sbin/sysctl -w %s=%d", vramSysctlKey, mb)
	appleScript := fmt.Sprintf(`do shell script %q with administrator privileges`, command)

	out, err := commandCombinedOutput("osascript", "-e", appleScript)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}

	return nil
}

func getCurrentVRAMLimitMB() (int, error) {
	out, err := commandOutput("sysctl", "-n", vramSysctlKey)
	if err != nil {
		return 0, err
	}

	val := strings.TrimSpace(string(out))
	mb, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("unexpected %s value: %q", vramSysctlKey, val)
	}

	if mb < 0 {
		return 0, nil
	}
	return mb, nil
}

func promptCustomVRAM() (int, error) {
	prompt := "Enter VRAM limit in MB (0 for Dynamic):"
	if maxVRAMLimitMB > 0 {
		prompt = fmt.Sprintf("Enter VRAM limit in MB (0-%d, where 0 = Dynamic):", maxVRAMLimitMB)
	}
	script := fmt.Sprintf(
		`text returned of (display dialog %q default answer "" buttons {"Cancel", "Apply"} default button "Apply")`,
		prompt,
	)
	out, err := commandCombinedOutput("osascript", "-e", script)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(msg), "user canceled") {
			return 0, errUserCanceled
		}
		if msg == "" {
			msg = err.Error()
		}
		return 0, errors.New(msg)
	}

	return parseCustomVRAMInput(string(out))
}

func parseCustomVRAMInput(raw string) (int, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, errors.New("value cannot be empty")
	}

	mb, err := strconv.Atoi(text)
	if err != nil {
		return 0, errors.New("value must be a whole number in MB")
	}

	if mb < 0 {
		return 0, errors.New("value must be >= 0")
	}

	return mb, nil
}

func getMaxVRAMLimitMB(totalMemMB int) int {
	if totalMemMB <= 0 {
		return 0
	}
	return int(math.Floor(float64(totalMemMB) * float64(maxPresetPercent) / 100))
}

func validateVRAMLimitMB(mb, totalMemMB int) error {
	if mb < 0 {
		return errors.New("value must be >= 0")
	}

	maxMB := getMaxVRAMLimitMB(totalMemMB)
	if maxMB > 0 && mb > maxMB {
		return fmt.Errorf("value exceeds max %d MB (%s)", maxMB, formatGBFromMB(maxMB))
	}

	return nil
}

func commandOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, name, args...).Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s command timed out", name)
	}
	return out, err
}

func commandCombinedOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s command timed out", name)
	}
	return out, err
}

func notifyVRAMResult(success bool, message string) {
	title := "MemVRAM: VRAM update failed"
	if success {
		title = "MemVRAM: VRAM updated"
	}
	go sendNotification(title, message)
}

func sendNotification(title, message string) {
	if runtime.GOOS != "darwin" {
		return
	}

	msg := strings.TrimSpace(message)
	if msg == "" {
		return
	}
	if len(msg) > 220 {
		msg = msg[:220] + "..."
	}

	script := fmt.Sprintf(`display notification %q with title %q`, msg, title)
	_, _ = commandCombinedOutput("osascript", "-e", script)
}

func onExit() {}
