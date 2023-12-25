# tsproxy

Tailscale proxy. This program allows to create a device that only represents a single port. By using this, you don't need to expose all the OS in the tailnet, but only the specified destination port.


To use this there're no need to have tailscale running on the host (not even installed). Just run the binary with the service ( address:port ) you wan't to expose on your tailnet as an argument and an authkey ( as an option or environment variable).

```
$ export TS_AUTHKEY=XXX
$ tsproxy --hostname plex --port 80 127.0.0.1:32400
```
