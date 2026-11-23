---
name: release
description: Building and distributing anvil (SPEC §13) — cross-platform builds, GoReleaser on GitHub Actions, go-gui packaging (.app/.exe with icon), AUR, Homebrew, winget, MIT license. Use when setting up CI, preparing a release or touching .goreleaser.yaml / workflows.
---

# Release and distribution

Goals: binaries on GitHub Releases for linux/amd64, linux/arm64, windows/amd64, darwin/amd64,
darwin/arm64; AUR, Homebrew and winget packages from the same pipeline. MIT license (`LICENSE` at the root).

## CGO: the real constraint

- Linux and Windows build with `CGO_ENABLED=0` (go-gui uses OpenGL through purego/xgb/syscall and SQLite is
  `glebarez/go-sqlite`, a fork of modernc). They can be cross-compiled from Linux.
- **macOS cannot**: go-gui's Metal backend is Objective-C (cgo, a go-gui decision) and needs the macOS SDK.
  Darwin binaries are built on a `macos-latest` runner with `CGO_ENABLED=1`. The SPEC says "no CGO on the
  3 platforms": that holds for SQLite, not for the UI on macOS.
- Consequence: the whole GoReleaser job runs on `macos-latest` with two build configs (`anvil` CGO-free for
  linux/windows, `anvil-darwin` with CGO). Not `--split`/`--merge` nor the `prebuilt` builder: those are
  GoReleaser Pro.
- **No signing**: no Apple Developer account (owner's decision). macOS binaries ship unsigned; the README
  documents `xattr -d com.apple.quarantine`. Do not add codesign/notarization steps.

## Packaging

- go-gui ships `cmd/buildapp` (see `~/go/pkg/mod/github.com/go-gui-org/go-gui@v0.92.0/docs/deployment.md`
  and `cmd/buildapp/README.md`): it takes an already built binary and produces the Windows `.zip` with an
  embedded icon, the Linux `.tar.gz` with a `.desktop` file, and the signed `.app` (+ `.dmg` with `-dmg`) on
  macOS. It reads `appinfo.toml` (`-manifest`); flags override the file.
  `go run github.com/go-gui-org/go-gui/cmd/buildapp@v0.92.0 -platform linux -o dist -version $V build/anvil`
  Use it as a GoReleaser post-hook or instead of `archives`. With a linked `.syso` it refuses to embed the
  icon: use one or the other.
- `appinfo.toml` defines the App ID (go-gui also uses it for settings/secrets).
- Windows signing (if ever): secrets in GitHub Actions, never in the repo.

## GoReleaser (skeleton)

```yaml
version: 2
project_name: anvil
builds:
  - id: anvil-cgo-free
    env: [CGO_ENABLED=0]
    goos: [linux, windows]
    goarch: [amd64, arm64]
    ignore: [{ goos: windows, goarch: arm64 }]
    ldflags: ["-s -w -X main.version={{.Version}}"]
  - id: anvil-darwin
    env: [CGO_ENABLED=1]
    goos: [darwin]
    goarch: [amd64, arm64]
archives:
  - formats: [tar.gz]
    format_overrides: [{ goos: windows, formats: [zip] }]
checksum: { name_template: checksums.txt }
aurs:        # PKGBUILD anvil-bin → AUR (SSH key in secrets)
homebrew_casks:  # own tap Watts-Argentina/homebrew-tap
winget:      # manifests → fork of microsoft/winget-pkgs (automatic PR)
```

Validate with `goreleaser check` and `goreleaser release --snapshot --clean` before tagging.

## Release checklist

1. `go vet ./... && go test ./...` green.
2. `CGO_ENABLED=0 GOOS=windows go build ./...` and `CGO_ENABLED=0 GOOS=linux go build ./...`.
3. Semver tag `vX.Y.Z` → triggers the workflow.
4. Check the release: 5 binaries + checksums, winget PR opened, AUR and tap updated.
5. Test the Linux binary on X11 and Wayland, and that the keyring works (or degrades as per SPEC §3.2).
