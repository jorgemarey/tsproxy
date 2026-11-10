# tsproxy

Tailscale proxy. This program allows to create a device that only represents a single port. By using this, you don't need to expose all the OS in the tailnet, but only the specified destination ports.


To use this there're no need to have tailscale running on the host (not even installed). Just run the binary with the services ( address:port ) you wan't to expose on your tailnet as arguments and an authkey ( as an option or environment variable).

```
$ export TS_AUTHKEY=XXX
$ tsproxy --hostname plex 80=127.0.0.1:32400
```

## Mappings

Each argument exposes a local service on the tailnet:

```
[PORT=]DEST[,tls]
```

- `DEST` is `HOST:PORT` (IPv6 hosts in brackets, e.g. `[::1]:8080`) or a unix socket, `unix:/path/to.sock`.
- `PORT` is the port on the tailnet. It defaults to the port of `DEST`, and is required for unix sockets.
- `,tls` serves the port over TLS using the device's Tailscale certificate (`https://<hostname>.<tailnet>.ts.net`). HTTPS and MagicDNS must be enabled for the tailnet.

The `-local` option (can be repeated) does the opposite: it exposes a tailnet service on a local listener, so applications next to the proxy can reach it without running Tailscale:

```
-local [LISTEN=]HOST:PORT
```

- `HOST:PORT` is an address on the tailnet (a MagicDNS name or a tailnet IP).
- `LISTEN` is a `PORT` (bound to `127.0.0.1`), a `HOST:PORT` or `unix:/path/to.sock`. It defaults to `127.0.0.1` and the tailnet port.

Examples:

```
# HTTP on 80 and HTTPS on 443 for a local web app, plus a service on a unix socket
$ tsproxy --hostname web 80=127.0.0.1:8080 443=127.0.0.1:8080,tls 9000=unix:/run/app.sock

# Reach a tailnet database on 127.0.0.1:5432 and a tailnet redis on a unix socket
$ tsproxy --hostname app -local db:5432 -local unix:/tmp/redis.sock=cache:6379
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
