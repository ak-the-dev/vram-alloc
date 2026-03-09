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
	updateInterval       = 3 * time.Second
	readCommandTimeout   = 5 * time.Second
	notifyCommandTimeout = 3 * time.Second
	sysctlPath           = "/usr/sbin/sysctl"
	osascriptPath        = "/usr/bin/osascript"
	vramSysctlKey        = "iogpu.wired_limit_mb"
	defaultVRAMLimitMB   = 0

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

type commandSpec struct {
	label   string
	path    string
	args    []string
	timeout time.Duration
}

type refreshSnapshot struct {
	hasRAM      bool
	usedBytes   uint64
	totalBytes  uint64
	usedPercent float64
	vramLimitMB int
}

var (
	isAppleSilicon bool
	ramDetailItem  *systray.MenuItem
	vramDetailItem *systray.MenuItem
	lastActionItem *systray.MenuItem

	totalMemoryMB  int
	maxVRAMLimitMB int
	updateMu       sync.Mutex

	virtualMemoryReader    = mem.VirtualMemory
	currentVRAMLimitReader = getCurrentVRAMLimitMB
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

	out, err := executeOutput(sysctlReadSpec("-n", "machdep.cpu.brand_string"))
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
			if maxVRAMLimitMB > 0 {
				item := systray.AddMenuItem(
					fmt.Sprintf("Max VRAM Limit: %d MB (%s)", maxVRAMLimitMB, formatGBFromMB(maxVRAMLimitMB)),
					"App safety cap at 90% of total memory",
				)
				item.Disable()
			}
		} else {
			item := systray.AddMenuItem(
				"Total Memory: unavailable",
				"VRAM controls are disabled until total memory can be detected",
			)
			item.Disable()
		}

		systray.AddSeparator()
		vramDetailItem = systray.AddMenuItem("VRAM: Detecting...", "Current GPU memory limit")
		vramDetailItem.Disable()

		if totalMemoryMB > 0 {
			menuHelp := fmt.Sprintf("Set 0..%d MB (admin required)", maxVRAMLimitMB)
			vramMenu := systray.AddMenuItem("Set VRAM Limit", menuHelp)

			resetDefault := vramMenu.AddSubMenuItem(
				"Reset to Default",
				"Restore the default macOS-managed VRAM configuration",
			)
			go func() {
				for range resetDefault.ClickedCh {
					applyDefaultVRAMLimit()
				}
			}()

			// Dynamic (Auto)
			dynamic := vramMenu.AddSubMenuItem("Dynamic (0 MB)", "Let macOS manage automatically")
			go watchVRAMSelection(dynamic, 0)

			for _, preset := range buildVRAMPresets(totalMemoryMB) {
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
			item := systray.AddMenuItem(
				"Set VRAM Limit: unavailable",
				"Controls are disabled because total memory could not be detected",
			)
			item.Disable()
		}
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
			setActionStatus(refreshStatusMessage(update()))
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
	_ = update()
	for range ticker.C {
		_ = update()
	}
}

func update() error {
	updateMu.Lock()
	defer updateMu.Unlock()

	snapshot, err := collectRefreshSnapshot(isAppleSilicon)
	if err != nil && !snapshot.hasRAM {
		systray.SetTitle("ERR")
		if ramDetailItem != nil {
			ramDetailItem.SetTitle("RAM stats unavailable")
		}
		if isAppleSilicon && vramDetailItem != nil {
			vramDetailItem.SetTitle("VRAM Limit: unavailable (check permissions)")
		}
		return err
	}

	systray.SetTitle(fmt.Sprintf("RAM: %.0f%%", math.Round(snapshot.usedPercent)))

	used := float64(snapshot.usedBytes) / 1024 / 1024 / 1024
	total := float64(snapshot.totalBytes) / 1024 / 1024 / 1024

	ramDetailItem.SetTitle(
		fmt.Sprintf("Used: %.1f / %.1f GB (%.0f%%)", used, total, snapshot.usedPercent),
	)

	if isAppleSilicon {
		if err != nil {
			vramDetailItem.SetTitle("VRAM Limit: unavailable (check permissions)")
			return err
		}
		setVRAMDetailTitle(snapshot.vramLimitMB)
	}

	return nil
}

func updateVRAMStatus() error {
	if vramDetailItem == nil {
		return nil
	}

	current, err := currentVRAMLimitReader()
	if err != nil {
		vramDetailItem.SetTitle("VRAM Limit: unavailable (check permissions)")
		return fmt.Errorf("refresh VRAM limit: %w", err)
	}

	setVRAMDetailTitle(current)
	return nil
}

func setVRAMDetailTitle(current int) {
	if vramDetailItem == nil {
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
	v, err := virtualMemoryReader()
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
	applyVRAMLimitWithSuccessMessage(mb, "")
}

func applyDefaultVRAMLimit() {
	applyVRAMLimitWithSuccessMessage(
		defaultVRAMLimitMB,
		"Reset to default configuration",
	)
}

func applyVRAMLimitWithSuccessMessage(mb int, successMessage string) {
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

	message := successMessage
	if message == "" {
		message = successMessageForVRAMLimit(mb)
	}
	setActionStatus(message)
	notifyVRAMResult(true, message)

	_ = updateVRAMStatus()
}

func successMessageForVRAMLimit(mb int) string {
	if mb == defaultVRAMLimitMB {
		return "Set to Dynamic (0 MB)"
	}
	return fmt.Sprintf("Set to %d MB (%s)", mb, formatGBFromMB(mb))
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

	command := fmt.Sprintf("%s -w %s=%d", sysctlPath, vramSysctlKey, mb)
	appleScript := fmt.Sprintf(`do shell script %q with administrator privileges`, command)

	out, err := executeCombinedOutput(interactiveAppleScriptSpec(appleScript))
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
	out, err := executeOutput(sysctlReadSpec("-n", vramSysctlKey))
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
	out, err := executeCombinedOutput(interactiveAppleScriptSpec(script))
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

	if totalMemMB == 0 && mb > 0 {
		return errors.New("value requires detected total memory")
	}

	maxMB := getMaxVRAMLimitMB(totalMemMB)
	if maxMB > 0 && mb > maxMB {
		return fmt.Errorf("value exceeds max %d MB (%s)", maxMB, formatGBFromMB(maxMB))
	}

	return nil
}

func collectRefreshSnapshot(appleSilicon bool) (refreshSnapshot, error) {
	v, err := virtualMemoryReader()
	if err != nil {
		return refreshSnapshot{}, fmt.Errorf("refresh RAM stats: %w", err)
	}

	snapshot := refreshSnapshot{
		hasRAM:      true,
		usedBytes:   v.Used,
		totalBytes:  v.Total,
		usedPercent: v.UsedPercent,
	}

	if !appleSilicon {
		return snapshot, nil
	}

	current, err := currentVRAMLimitReader()
	if err != nil {
		return snapshot, fmt.Errorf("refresh VRAM limit: %w", err)
	}

	snapshot.vramLimitMB = current
	return snapshot, nil
}

func sysctlReadSpec(args ...string) commandSpec {
	return commandSpec{
		label:   "sysctl",
		path:    sysctlPath,
		args:    args,
		timeout: readCommandTimeout,
	}
}

func interactiveAppleScriptSpec(script string) commandSpec {
	return commandSpec{
		label: "osascript",
		path:  osascriptPath,
		args:  []string{"-e", script},
	}
}

func notificationAppleScriptSpec(script string) commandSpec {
	return commandSpec{
		label:   "osascript",
		path:    osascriptPath,
		args:    []string{"-e", script},
		timeout: notifyCommandTimeout,
	}
}

func executeOutput(spec commandSpec) ([]byte, error) {
	if spec.timeout <= 0 {
		return exec.Command(spec.path, spec.args...).Output()
	}

	ctx, cancel := context.WithTimeout(context.Background(), spec.timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, spec.path, spec.args...).Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s command timed out", spec.label)
	}
	return out, err
}

func executeCombinedOutput(spec commandSpec) ([]byte, error) {
	if spec.timeout <= 0 {
		return exec.Command(spec.path, spec.args...).CombinedOutput()
	}

	ctx, cancel := context.WithTimeout(context.Background(), spec.timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, spec.path, spec.args...).CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s command timed out", spec.label)
	}
	return out, err
}

func refreshStatusMessage(err error) string {
	if err != nil {
		return fmt.Sprintf("Manual refresh failed: %v", err)
	}
	return "Manual refresh completed"
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
	_, _ = executeCombinedOutput(notificationAppleScriptSpec(script))
}

func onExit() {}
