# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - Unreleased

### Breaking changes

- **Mappings replace `-port`.** Each positional argument is now a mapping, `[PORT=]DEST[,tls]`. Use `80=127.0.0.1:8080` instead of `-port 80 127.0.0.1:8080`.
- **Devices are ephemeral by default, and `-ephemeral` is removed.** Without `-dir`, state is kept in memory and the device logs out on shutdown, so it's removed from the tailnet right away. With `-dir`, the device is persistent and keeps its identity in that directory. Use a reusable authkey for ephemeral devices, since every start registers a new device.
- **`-disable-ts-logs` is removed.** It had no effect. Tailscale's logs now go through the proxy's logger (see `-log-level`), and `-no-log-upload` stops uploading them to Tailscale.
- **New exit codes.** `-h` exits with status 0 and invalid flags exit with status 2. Errors are printed to stderr.

### Added

- **Multiple mappings.** Expose several ports from one device, e.g. `80=127.0.0.1:8080 443=127.0.0.1:8080,tls`.
- **TLS.** The `,tls` suffix serves a tailnet port over TLS with the device's Tailscale certificate. HTTPS and MagicDNS must be enabled for the tailnet.
- **Unix sockets.** A destination can be a unix socket, e.g. `9000=unix:/run/app.sock`.
- **`-local [LISTEN=]HOST:PORT` (repeatable).** Exposes a tailnet service on a local TCP address or unix socket, so applications next to the proxy can reach the tailnet without running Tailscale.
- **IPv6 destinations** in brackets, e.g. `[::1]:8080`.
- **Flags anywhere.** Flags can appear after mappings, and arguments after `--` are always mappings.
- **`-tags tag:a,tag:b`** advertises tags for the device.
- **OAuth client secrets** (`tskey-client-...`) as the authkey. They require `-tags`.
- **`-authkey-file`** reads the authkey from a file. `TS_AUTHKEY` is still the fallback.
- **Wait for the tailnet before listening.** The proxy connects before it listens, so a bad authkey or an unreachable control server fails at startup. Once connected, it logs the device's MagicDNS name and tailnet IPs.
  - `-up-timeout` (default 1m, `0` to wait forever) bounds the wait.
- **Graceful shutdown on `SIGINT` and `SIGTERM`.** The proxy stops accepting connections and waits up to `-shutdown-timeout` (default 5s) for open ones to finish, then leaves the tailnet. A second signal exits immediately.
- **`-health-addr` serves HTTP health checks.**
  - `/healthz` answers 200 while the process is running.
  - `/readyz` answers 200 while the proxy is listening and connected to the tailnet, and 503 while connecting or shutting down.
- **`-webui`** runs the Tailscale web interface for the device on tailnet port 5252.
- **`-no-log-upload`** opts out of uploading Tailscale logs to Tailscale.
- **Release binaries for arm64** (linux, windows and darwin).

### Changed

- **Updated `tailscale.com`** from v1.56.1 to v1.104.5. Go 1.27.3 is required to build.
- **Structured Tailscale logs.** Tailscale's messages are logged through `log/slog`, tagged `component=tailscale`. Status messages are logged at `info` and verbose internal logs at `debug`.
- **Dial timeout.** Connecting to a destination times out after 10 seconds.
- **Startup validation.** Mappings are validated before Tailscale starts. Duplicate listen addresses, and the port used by `-webui`, are rejected.
- **Release builds.** Binaries are built with the Go version from `go.mod` and stripped of debug information.

### Fixed

- **Authkey shown in help.** `-h` printed the authkey from `TS_AUTHKEY` as the default value of `-authkey`.
- **Lost responses.** A client that half-closed its connection after sending a request could lose the response, because both connections were closed as soon as one direction finished.
- **Busy loop on accept errors.** The accept loop retried errors immediately, flooding the logs. Accept errors are now retried with exponential backoff, and the loop stops when the listener closes.
- **No clean shutdown.** The proxy had no signal handling and never disconnected from the tailnet or flushed its logs on exit.
- **Missing destination.** Running without a destination started a proxy whose connections all failed.
- **Bad destinations.** IPv6 destinations, and destinations without a valid port, silently fell back to port 8080.
- **New device on every restart.** Non-ephemeral devices without `-dir` registered a new device on each restart. Their temporary state directories were also never removed.

## [0.1.0] - 2023-12-26

### Added

- **Initial release.** Exposes a single local TCP service (`HOST:PORT`) as its own Tailscale device using `tsnet`, without Tailscale installed on the host.
- **`-port`** sets the tailnet port. It defaults to the destination's port, or 8080.
- **`-hostname`** sets the device name.
- **`-authkey`**, or `TS_AUTHKEY`, sets the authkey.
- **`-dir`** sets the state directory.
- **`-control-url`** sets a custom coordination server.
- **`-ephemeral`** registers the device as ephemeral, with its state kept in memory.
- **`-disable-ts-logs`** was meant to disable Tailscale logs.
- **`-log-level`** sets the log level.
- **`-version`** prints the version.
- **Structured logging** with `log/slog`.
- **Release binaries** for linux, windows and darwin on amd64.

[0.2.0]: https://github.com/jorgemarey/tsproxy/compare/v0.1.0...main
[0.1.0]: https://github.com/jorgemarey/tsproxy/releases/tag/v0.1.0
