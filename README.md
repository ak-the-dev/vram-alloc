# MemVRAM

Menu bar app for monitoring RAM usage and applying Apple Silicon VRAM limits via `iogpu.wired_limit_mb`.

## Features

- Real-time RAM usage in the macOS menu bar.
- Current VRAM limit display (`Dynamic` when set to `0`).
- Current VRAM limit percentage of total memory.
- Quick VRAM presets (5% to 90% of total memory, capped at 90%).
- Custom VRAM value prompt (in MB).
- Input validation to prevent values above the app safety cap.
- Admin-authenticated VRAM updates through macOS permission dialog.
- `Refresh Now` tray action for immediate status refresh.
- Desktop notifications for VRAM apply success/failure.

## Requirements

- macOS on Apple Silicon (`darwin/arm64`) for VRAM controls.
- Go 1.23+ for development.

Notes:
- On non-Apple Silicon systems, the app still shows RAM usage but VRAM controls are disabled.
- Changing VRAM limit requires admin authentication and may not be available on all macOS versions/configurations.

## Run Locally

```bash
go run .
```

## Build

```bash
make build
```

## Test

```bash
go test ./...
```

## Project Layout

- `main.go`: app runtime, tray UI, sysctl integration.
- `main_test.go`: unit tests for preset generation and custom input parsing.
- `.github/workflows/ci.yml`: GitHub Actions CI.

## GitHub Setup Checklist

1. Push this repository to `github.com`.
2. Confirm your module path in `go.mod` matches your final repository URL.
3. Ensure CI passes on your default branch.
4. Create a release and attach the macOS binary if you distribute builds.

## License

MIT (see [LICENSE](LICENSE)).
