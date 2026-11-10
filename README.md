# tsproxy

Tailscale proxy. This program allows to create a device that only represents a single port. By using this, you don't need to expose all the OS in the tailnet, but only the specified destination port.


To use this there're no need to have tailscale running on the host (not even installed). Just run the binary with the service ( address:port ) you wan't to expose on your tailnet as an argument and an authkey ( as an option or environment variable).

```
$ export TS_AUTHKEY=XXX
$ tsproxy --hostname plex --port 80 127.0.0.1:32400
```

## Ephemeral and persistent devices

By default the proxy joins the tailnet as an **ephemeral** device: its state is kept in memory, and on shutdown it logs out so the device is removed from the tailnet right away. Every start registers a new device (with a new tailnet IP), so:

- use a **reusable** authkey, since a single-use key only works for the first start;
- reach the proxy by its MagicDNS name rather than its IP.

To keep the same device and IP across restarts, pass a state directory with `-dir`. The device is then **persistent** and its identity is stored in that directory (in a container, mount a volume there):

```
$ tsproxy --hostname plex --port 80 --dir /var/lib/tsproxy/plex 127.0.0.1:32400
```

Each proxy instance needs its own state directory.
