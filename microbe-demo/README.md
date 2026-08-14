# microbe demo

`microbe`: static Go binary built from `../iso`. `microbe.nix`: 3-VM stack —
`db` (postgres, healthcheck on 5432), `web` (httpd, depends on db), `jump`
(no static addr, auto-allocated, has a folder-bind volume sharing `./shared`
into `/shared` — the default volume type, runs unprivileged, see below).
Every service is IPv6-only, sharing one flat address space (this host's own
randomly-generated ULA `/64`); `rules:` in `microbe.nix` is what lets `web`
reach `db` at all -- everything else is default-deny.

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

This also writes `flake.nix`, `generated.json`, `microbe.lock.json` and
`parts/*.nix` into this directory, alongside `microbe.nix` — the real Nix
the stack runs, rendered from `microbe.nix` and safe to read (or
git-track). They're only rewritten when their content actually changes, so
an `up` with no config changes leaves them untouched.
`microbe.lock.json` is the permanent record of each service's IPv6
address (drawn from this host's own randomly-generated ULA `/64`,
`/var/lib/microbe/host-state.json`) — generated once on first `up` and
never changed again after that, like a `Cargo.lock`. Commit it for real
so the addresses stay stable across machines/clones.

`flake.nix` follows the Dendritic pattern (flake-parts + import-tree):
every file under `parts/` self-registers — `renderer.nix`/`guest-base.nix`
into `flake.nixosModules.*`, each service's own `<svc>.nix` into
`flake.nixosConfigurations.<svc>` — instead of being wired by an explicit
imports list.

## Check status

```
sg microbe -c './microbe ps'
```

`ps` prints each service's address; `microbe config` prints the full
network plan (address + MACs) if you want it before starting anything.

## Verify it's real

```
# look up db's and web's addresses (or read microbe.lock.json directly)
sg microbe -c './microbe ps'

# postgres, over the network, no ssh (swap in db's actual address):
nix shell nixpkgs#postgresql -c psql -h <db-address> -p 5432 -U postgres -d postgres -c 'select 1;'

# web's Apache, direct to the guest address (published-port DNAT can't be
# self-tested from this same host - see below):
curl -g "http://[<web-address>]:80/"

# outbound: guests reach the IPv4 internet via NAT64+DNS64 (tayga/unbound,
# see modules/host.nix's nat64 option), the IPv6-only equivalent of a
# docker bridge network's masquerade:
microbe shell db
ping -c1 google.com

# folder-bind volume (virtiofs): jump's /shared is ./shared on the host,
# mounted unprivileged (a companion virtiofsd process, no root needed):
microbe exec jump -- cat /shared/hello.txt
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

## Try the default-deny rules (the other interesting part)

`jump` has no `rules:` entry granting it access to `db`, so it can't reach
postgres at all:

```
microbe exec jump -- nc -zv -w2 <db-address> 5432   # should fail/timeout
microbe exec web  -- nc -zv -w2 <db-address> 5432   # should succeed (rules: web -> db)
```

## Notes

- Published ports (5432, 8080) are DNAT'd via nftables straight to the
  guest's real IPv6 address — reachable from another IPv6-capable machine
  on the LAN, but *not* via `localhost`/the host's own address from this
  same host (a Linux hairpin-NAT quirk, not a microbe bug). Use the direct
  guest address to test locally, as above. Reachability from an IPv4-only
  external client isn't implemented yet (see `modules/host.nix`'s
  `nat64.enable` doc comment).
- Runtime data (build outputs, run dirs, logs, volumes, `state.json`) lives
  under `/var/lib/microbe/test-net/` (the stack's `name` from `microbe.nix`),
  not in this directory — daemon-owned, group `microbe`, same idea as
  `/var/lib/docker`. `down --remove-volumes` cleans it up; nothing in this
  directory needs deleting.
