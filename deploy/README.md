# Running a fleet

Two units live here, and the difference between them is the whole point
of the fleet's shape. `bifrost.service` is one machine with state, made
by hand and kept; `heimdall.service` is a machine with none, which is
what makes it something a scaling loop can buy and throw away.

Build both on any machine with Go, including the one you develop on:

```
make cross            # dist/bifrost-linux-amd64, dist/heimdall-linux-amd64
```

`CGO_ENABLED=0` is set in the Makefile, so these are static binaries and
the target needs nothing installed on it.

## The coordinator, once

Its address is the one fixed thing every relay and every agent is
configured with, so this machine is built once and not rebuilt. Copy the
binary and the unit up, then:

```
useradd --system --no-create-home --shell /usr/sbin/nologin bifrost
install -m755 bifrost-linux-amd64 /usr/local/bin/bifrost
install -m644 bifrost.service /etc/systemd/system/bifrost.service
systemctl daemon-reload && systemctl enable --now bifrost
journalctl -u bifrost -n 20
```

The last line prints the multiaddrs to give everything else. If they
name a private address, the machine cannot see its own public one, and
`BIFROST_ANNOUNCE` in the unit is the answer.

Open UDP and TCP 4002 to the world. The admin API is on loopback and has
no authentication, so reach it through SSH rather than opening it:

```
ssh -N -L 4080:127.0.0.1:4080 root@<coordinator>
```

Back up `/var/lib/bifrost`. Losing `identity.key` means reconfiguring
every relay and every agent.

## One relay, by hand, before any of them are cloned

Same two files, plus the coordinator's address in
`HEIMDALL_BIFROST`. Open UDP and TCP 4001, and TCP 443 as well — a relay
that only answers on 4001 is no use to someone on a network that passes
nothing else.

```
install -m755 heimdall-linux-amd64 /usr/local/bin/heimdall
install -m644 heimdall.service /etc/systemd/system/heimdall.service
systemctl daemon-reload && systemctl enable --now heimdall
journalctl -u heimdall -f
```

`registered with the coordinator` is the whole check. If it says that,
`GET /v1/relays` on the admin API lists this machine, and an agent
pointed at the coordinator will be placed on it.

Then measure what the machine can really forward, per direction, and put
that number in `HEIMDALL_BANDWIDTH`. Every placement decision the
coordinator makes is arithmetic on it, so a guess there is a guess
everywhere.

## Only then, the image

A relay in a fleet writes nothing, which is what makes this safe: there
is no key on the disk to be copied into every clone, and no state to go
stale in an image. Prepare one machine exactly as above, stop it, and it
is the source `internal/provision/depa` clones.

Leave `HEIMDALL_ANNOUNCE` out of that image. It replaces the whole
advertised address set, and every clone gets a different address, so a
value baked in here is wrong on all of them.

## Checking the provider without a fleet

`deploy/depa` is the same account seen by hand: `relays` lists what the
fleet owns, `all` lists everything including machines that are nobody's
business of ours, and `clone` and `destroy` do one at a time what the
scaler will do in a loop. It needs `curl`, `jq` and `DEPA_API_KEY`.

The adapter has its own live tests, behind a build tag so they never run
in the normal suite. The read-only ones cost nothing:

```
DEPA_API_KEY=... DEPA_SOURCE=<instance uuid> \
  go test -tags live ./internal/provision/depa/ -v -count=1
```

The round trip clones a machine and destroys it again, which costs a few
minutes of one instance and is the only way to find out whether creating
and destroying work at all:

```
DEPA_API_KEY=... DEPA_SOURCE=<instance uuid> DEPA_LIVE_CLONE=1 \
  go test -tags live ./internal/provision/depa/ -v -count=1 -timeout 25m
```

If that test ever fails after the clone succeeded, read the failure: it
prints the uuid of anything it could not destroy, and a machine nobody
destroys bills until somebody notices.
