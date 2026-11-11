# tsproxy

Tailscale proxy. This program allows to create a device that only represents a single port. By using this, you don't need to expose all the OS in the tailnet, but only the specified destination ports.


To use this there're no need to have tailscale running on the host (not even installed). Just run the binary with the services ( address:port ) you wan't to expose on your tailnet as arguments and an authkey ( as an option or environment variable).

```
$ export TS_AUTHKEY=XXX
$ tsproxy --hostname plex 80=127.0.0.1:32400
```

The authkey can also be read from a file with `-authkey-file`, which keeps it out of the process arguments and environment.

### Tags and OAuth clients

Devices get the tags of the authkey they register with. `-tags tag:a,tag:b` additionally advertises tags from the proxy; the control server decides whether the device can take them, based on the tag owners in your tailnet policy.

The authkey can also be an [OAuth client](https://tailscale.com/kb/1215/oauth-clients) secret (`tskey-client-...`). On every start the proxy uses it to create a new single-use authkey with the tags given in `-tags`, which is then required. OAuth client secrets don't expire like authkeys do, so they are a good fit for long-running proxies. Options can be appended to the secret:

- `?ephemeral=false`: the generated keys are ephemeral by default, so add this for persistent devices (`-dir`).
- `?preauthorized=true`: skip device approval, if it's enabled in your tailnet.

```
$ tsproxy --hostname plex --tags tag:proxy --authkey-file /run/secrets/ts-oauth 80=127.0.0.1:32400
```

## Mappings

A mapping listens on one side and forwards connections to the other. There are two kinds, named after where they listen, and both options can be repeated:

- `-tailnet` listens **on the tailnet** and exposes a local service there.
- `-local` listens **locally** and exposes a tailnet service, so applications next to the proxy can reach it without running Tailscale.

### `-tailnet [PORT=]DEST[,tls]`

- `DEST` is `HOST:PORT` (IPv6 hosts in brackets, e.g. `[::1]:8080`) or a unix socket, `unix:/path/to.sock`.
- `PORT` is the port on the tailnet. It defaults to the port of `DEST`, and is required for unix sockets.
- `,tls` serves the port over TLS using the device's Tailscale certificate (`https://<hostname>.<tailnet>.ts.net`). HTTPS and MagicDNS must be enabled for the tailnet.

Arguments are the same as `-tailnet`, so `tsproxy 80=127.0.0.1:8080` and `tsproxy -tailnet 80=127.0.0.1:8080` are equivalent.

### `-local [LISTEN=]HOST:PORT`

- `HOST:PORT` is an address on the tailnet (a MagicDNS name or a tailnet IP).
- `LISTEN` is a `PORT` (bound to `127.0.0.1`), a `HOST:PORT` or `unix:/path/to.sock`. It defaults to `127.0.0.1` and the tailnet port.

### Examples

```
# HTTP on 80 and HTTPS on 443 for a local web app, plus a service on a unix socket
$ tsproxy --hostname web -tailnet 80=127.0.0.1:8080 -tailnet 443=127.0.0.1:8080,tls -tailnet 9000=unix:/run/app.sock

# Reach a tailnet database on 127.0.0.1:5432 and a tailnet redis on a unix socket
$ tsproxy --hostname app -local db:5432 -local unix:/tmp/redis.sock=cache:6379

# Both: expose an app on the tailnet, and let it reach a tailnet database
$ tsproxy --hostname app -tailnet 80=127.0.0.1:8080 -local 5432=db:5432
```

Unix socket paths can't contain `=` or end in `,tls`.

## Ephemeral and persistent devices

By default the proxy joins the tailnet as an **ephemeral** device: its state is kept in memory, and on shutdown it logs out so the device is removed from the tailnet right away. Every start registers a new device (with a new tailnet IP), so:

- use a **reusable** authkey, since a single-use key only works for the first start;
- reach the proxy by its MagicDNS name rather than its IP.

To keep the same device and IP across restarts, pass a state directory with `-dir`. The device is then **persistent** and its identity is stored in that directory (in a container, mount a volume there):

```
$ tsproxy --hostname plex --dir /var/lib/tsproxy/plex 80=127.0.0.1:32400
```

Each proxy instance needs its own state directory.

## Container image

Images for `linux/amd64` and `linux/arm64` are published to `ghcr.io/jorgemarey/tsproxy`, tagged with the release version (`0.2.0`), the minor version (`0.2`) and `latest`. The image runs as a non-root user (uid 65532), and its entrypoint is `tsproxy`, so the arguments are the usual options and mappings.

Destinations like `127.0.0.1:8080` are resolved inside the proxy's container, so run it as a sidecar sharing the application's network (`--network container:<app>`, or the same pod in Kubernetes), or point the mappings at the application's address:

```
$ docker run -d --name plex-proxy --network container:plex -e TS_AUTHKEY \
    ghcr.io/jorgemarey/tsproxy:0.2 --hostname plex 80=127.0.0.1:32400
```

For a persistent device, mount a volume on `/var/lib/tsproxy`, which the non-root user can write to, and pass `-dir /var/lib/tsproxy`:

```
$ docker run -d --name plex-proxy --network container:plex -e TS_AUTHKEY \
    -v plex-proxy:/var/lib/tsproxy \
    ghcr.io/jorgemarey/tsproxy:0.2 --hostname plex --dir /var/lib/tsproxy 80=127.0.0.1:32400
```

## Web interface

`-webui` runs the Tailscale web interface for the device on tailnet port 5252 (`http://<hostname>:5252`), where tailnet users can see its status and manage it. Access follows your tailnet's access rules, and changes require re-authenticating. Mappings can't use port 5252 while it's enabled.

## Logging

All output is structured (`log/slog` text format) on stderr. `-log-level` sets the level (`debug`, `info`, `warn`, `error`). Tailscale's own status messages are logged at `info` and its verbose internal logs at `debug`, both tagged with `component=tailscale`.

Like the regular Tailscale client, tsproxy uploads its Tailscale logs to Tailscale for troubleshooting. Use `-no-log-upload` to opt out; Tailscale can't provide support for devices that do.

## Health checks

`-health-addr` (e.g. `127.0.0.1:9002`) serves HTTP health checks for supervisors and load balancers:

- `/healthz` answers `200` while the process is running.
- `/readyz` answers `200` once the proxy is listening and the device is connected to the tailnet, and `503` otherwise, including while shutting down.

## Startup and shutdown

On startup the proxy waits for the device to connect to the tailnet before listening, and exits if that takes longer than `-up-timeout` (1 minute by default, `0` to wait forever), so a wrong authkey or an unreachable control server fails right away.

On `SIGINT` or `SIGTERM` it stops accepting connections and shuts down within `-shutdown-timeout` (5 seconds by default), counted from the signal:

1. Open connections get until 1 second before the timeout to finish (half of the timeout, if it's shorter than 2 seconds).
2. The ones still open are closed. Long-lived connections, like HTTP keep-alive or streaming, usually don't finish on their own, so they end here.
3. The device leaves the tailnet (ephemeral devices log out, so they are removed right away) with the time left.

Set `-shutdown-timeout` below the time your supervisor waits before killing the process (`docker stop`: 10s, Kubernetes `terminationGracePeriodSeconds`: 30s, Nomad `kill_timeout`: 5s, systemd `TimeoutStopSec`). Otherwise the process can be killed before it leaves the tailnet, and an ephemeral device stays in the tailnet until the control server removes it. With Nomad's default, use `-shutdown-timeout 4s` or raise `kill_timeout`.

A second signal exits immediately.
