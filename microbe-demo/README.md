# microbe demo

`microbe`: static Go binary built from `../iso`. `microbe.nix`: 3-VM stack —
`db` (postgres, healthcheck on 5432), `web` (httpd, depends on db), `jump`
(no static IP, auto-allocated).

Requires membership in the `microbe` group (talks to the root
`microbe-provisiond` daemon over `/run/microbe.sock`); no sudo needed. If
your shell doesn't already have that group, prefix commands with
`sg microbe -c '...'`.

(`microbe.nix` here is checked in already; `microbe init` is how you'd
scaffold a new project's starter `microbe.nix` from scratch.)

## Bring the stack up

```
sg microbe -c './microbe up'
```

Expect: `rendered .`, three `nix build` lines, `volume ...
db-data.qcow2`, `started db (pid ...)`, `healthy db` (waits for postgres to
actually accept connections — a few seconds), `started jump`, `started web`,
then a table showing all three `running`/`healthy`.

This also writes `flake.nix`, `generated.json` and `modules/*.nix` into this
directory, alongside `microbe.nix` — the real Nix the stack runs, rendered
from `microbe.nix` and safe to read (or git-track). They're only rewritten
when their content actually changes, so an `up` with no config changes
leaves them untouched.

## Check status

```
sg microbe -c './microbe ps'
```

## Verify it's real

```
# postgres, over the network, no ssh:
nix shell nixpkgs#postgresql -c psql -h 192.168.51.2 -p 5432 -U postgres -d postgres -c 'select 1;'

# web's Apache, direct to the guest IP (published-port DNAT can't be
# self-tested from this same host - see below):
curl http://192.168.51.3:80/

# outbound: guests reach the internet via masquerade (default, like a
# docker bridge network):
microbe shell db
ping -c1 1.1.1.1
ping -c1 google.com
```

## Tear down

```
sg microbe -c './microbe down --remove-volumes'
```

## Try the healthcheck gating (the interesting part)

Edit `microbe.nix`, change `healthcheck.port` under `db` from `5432` to
something nothing listens on (e.g. `5433`), then:

```
sg microbe -c './microbe down --remove-volumes'
sg microbe -c './microbe up'
```

Expect: `started db`, then after ~10s `degraded db: service "db" did not
become healthy within 10s`, non-zero exit, and `jump`/`web` never start
(`ps` shows them `stopped`, no PID) — `web`'s `dependsOn = [ "db" ]` actually
gates on health, not just process-start. Change the port back to `5432` and
`up` again to confirm it recovers.

## Notes

- Published ports (5432, 8080) are DNAT'd via nftables — reachable from
  another machine on the LAN, but *not* via `localhost`/the host's own IP
  from this same host (`ip route get <own-ip>` shows the kernel routes that
  through `lo`, bypassing the nftables prerouting hook — a Linux hairpin-NAT
  quirk, not a microbe bug). Use the direct guest IP (`192.168.51.x`) to
  test locally, as above.
- Runtime data (build outputs, run dirs, logs, volumes, `state.json`) lives
  under `/var/lib/microbe/test-net/` (the stack's `name` from `microbe.nix`),
  not in this directory — daemon-owned, group `microbe`, same idea as
  `/var/lib/docker`. `down --remove-volumes` cleans it up; nothing in this
  directory needs deleting.
