# Bringing the first fleet up

A running log of what has actually been observed, kept because most of
what was learned came from things going wrong in ways that looked like
success. Last touched 10 Sep 2026. It is not a design document —
`FLEET.md` is — and it can be deleted once the fleet has carried a byte.

## Where it stands

The coordinator works. A relay installed by hand works. **No clone has
ever had a working network**, and that is the one thing standing between
here and an autoscaling fleet.

## The machines, on DEPA Cloud

| what | uuid | address | state |
|---|---|---|---|
| `bifrost` | `8dacdaa6-1bc6-4cbf-ac73-8dee11fb2aec` | 103.150.61.13 | running, healthy |
| `relay-source` | `79c99202-4190-4e56-bf44-1b14abd05dca` | 103.150.61.215 | stopped, prepared as the image |
| `heimdall-3` | `09d8a355-4338-4abd-98f2-8b75af58d539` | 103.253.244.194 | a clone with no network — destroy it |

The coordinator answers at

```
/ip4/103.150.61.13/udp/4002/quic-v1/p2p/12D3KooWDYbJetxxbGaZiNK61S4WjBsbghTvsuvttDsrWPmLTqem
```

Its admin API is on loopback there: `ssh root@103.150.61.13 'curl -s
localhost:4080/v1/relays'`.

Both API keys used so far were temporary and are in a chat transcript.
Neither should still exist.

## What has been observed to work

The adapter's read path, against the real account: `List`, `Sizes` and
`Regions` return what the account holds, and the fields are spelled the
way the adapter reads them. `internal/provision/depa`'s live tests are
the check and they pass.

A bad key, and a key whose scope does not cover the call, both come back
as HTTP 400 — `TOKEN_NOT_FOUND` and `SCOPE_MISMATCH`. Both classify as
fatal, which is what a coordinator holding the wrong key should do.

`deploy/bootstrap` makes the two machines the fleet cannot make for
itself, and makes neither of them twice. `deploy/install` puts the
coordinator on one and a relay on the other, and the relay registers: it
appeared in `GET /v1/relays` with its own address, `103.150.61.215`,
carrying TCP 443 as well as 4001, so the capability that lets it bind 443
works.

The ephemeral relay key works. `relay-source`'s peer id changed across a
reinstall, exactly as designed, and the coordinator took the new one
without complaint.

The provider's default firewall policy is ACCEPT with no rules, so there
is nothing to open.

Cloning and destroying both work as API calls: a machine appears, and a
machine goes away leaving nothing behind.

## What is not true yet

**A clone has never joined the network.** Three of them, and from a
machine on the same subnet the address does not even answer ARP, so it is
not a service that failed to start — nothing claims the address at all.

Everything downstream of that is therefore unproven: no agent has leased
a relay from this fleet, no byte has moved through it, and
`HEIMDALL_BANDWIDTH` is still the 50M placeholder rather than a
measurement.

One earlier claim was too strong and is withdrawn. The live test that
"proved cloning works" only proved the API call: it checked that a
machine appeared in the listing and was destroyed again. It never checked
that the machine was reachable.

## What was tried

**Cloning a stopped source rather than a running one.** The design says
the source should be stopped, and it was a reasonable guess that a disk
copied mid-write would not boot. It made no difference: the clone of the
stopped source was just as unreachable.

**Replacing the image's MAC pin.** The provider's Ubuntu image pins the
interface to the source machine's MAC address in netplan, and a clone
gets a new MAC, so the match fails and nothing brings the interface up.
That is certainly a bug and it is certainly fixed — `deploy/install` now
matches on interface name, tells cloud-init to leave networking alone,
and clears the machine id, the ssh host keys and cloud-init's instance
record. It was applied to `relay-source` at 19:53 and the clone was made
at 19:56, so the clone had it. It was not enough on its own.

## What to try next

**Look at the screen.** The provider offers a VNC console. Open
`heimdall-3` in the dashboard and see where the boot stops: a login
prompt means the network is the whole problem, an initramfs shell means
the filesystem is, and a blank screen means something earlier.

**Clone `bifrost` as a control.** Nothing has been done to that machine —
no image preparation, no edited netplan. If its clone has no network
either, then clones on this provider simply do not get one and the
approach needs rethinking. If its clone works, the image preparation
broke something. It costs about Rp30 and it is the experiment that
separates "the provider" from "our doing".

Do the free one first.

## Bugs this bring-up found

Four, all of which looked like success at the time.

`deploy/install` opened its ssh control socket at a path with no `%C` in
it, so every machine in a run shared one connection and the relay was
installed on the coordinator. The only sign was a password asked once
where it should have been asked twice. Fixed, and the relay install now
asks both ends their hostname and refuses if they match.

The same socket path was then too long for a unix socket on macOS, where
`TMPDIR` is a long path under `/var/folders`. Fixed by putting it under
`/tmp`.

`Destroy` would have accepted the uuid of the instance every relay is
cloned from. Worse, the source sits stopped, and stopped maps to `Gone`,
so a correctly working reconcile would have destroyed it. It now refuses
that uuid, and `deploy/bootstrap` refuses to name either hand-built
machine with the fleet's prefix.

The listing was narrowing itself with the provider's `search` parameter,
whose behaviour is documented rather than observed. A search matching too
little would hide a relay from the reconcile and the scaler would buy
another. It lists the whole account and filters locally instead.
