# microbe — Docker-Compose-Style Orchestration for microvm.nix

Status: **Draft for review** · Version 2.0 (supersedes v1; adds formal schema + microvm.nix integration spec)
Date: 2026-08-07

---

## 1. Overview & Motivation

`microvm.nix` makes it easy to declare and run a single MicroVM on NixOS.
What is missing today is *orchestration of multiple VMs as a unit*: shared
virtual networks, persistent volumes that survive reboots, startup ordering,
port publishing, and a stack-level lifecycle (`up` / `down` / `ps` / `logs`).

This project adds a **Go CLI** named `microbe` that treats microvm.nix
the way docker-compose treats Docker: one declarative file (`microbe.nix`)
describes a stack of VMs, and the CLI renders, provisions, and manages them.

Key design decision: **the config file is written in Nix**, not YAML/JSON.
Nix is already the language of this stack and gives us module reuse, functions,
and build-time guarantees. The Go CLI does **not** parse Nix itself; it shells
out to `nix-instantiate --eval --json` to lower the config to JSON, then
orchestrates from that.

**This document is the authoritative reference** for (a) the structure of
`microbe.nix` and (b) how that structure integrates into microvm.nix.
Sections §4–§9 form the formal specification; the rest are project goals,
UX, and execution plan.

---

## 2. Goals

| # | Goal |
|---|------|
| G1 | Declare an entire multi-VM stack in one `microbe.nix` file. |
| G2 | `up` / `down` / `ps` / `logs` / `exec` lifecycle for the whole stack. |
| G3 | Named, persistent block-device volumes (survive `down` / `up`). |
| G4 | Host-path shares (virtiofs / 9p) for bidirectional file exchange. |
| G5 | Declarative virtual networks with static IPs and cross-VM connectivity. |
| G6 | Port publishing from host → guest. |
| G7 | Startup ordering (`dependsOn`) and optional healthchecks. |
| G8 | Zero hand-written per-VM networking config: guest netconfig is generated. |
| G9 | Reproducible: everything the VMs run comes from the Nix store. |

Non-goals for v1 (candidates for v2):
- Live hot-reload / config watching.
- Rolling updates / zero-downtime migrations.
- GPU / SR-IOV passthrough.
- Remote (non-local) hosts.

---

## 3. Architecture Overview

```
┌──────────────────────────────────────────────────────────────┐
│                        user terminal                          │
│                       microbe <cmd>                     │
└───────────────────────────┬──────────────────────────────────┘
                            │
┌───────────────────────────▼──────────────────────────────────┐
│                        Go CLI  (microbe)                │
│                                                               │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────┐   │
│  │  config load │  │ state store  │  │  orchestrator      │   │
│  │  (eval→JSON) │  │  (local db)  │  │  (up/down/ps/logs) │   │
│  └──────┬───────┘  └──────┬───────┘  └─────────┬──────────┘   │
│         │                 │                    │              │
│  ┌──────▼───────┐  ┌──────▼───────┐  ┌─────────▼──────────┐   │
│  │  nix render  │  │  host net    │  │  runtime adapter   │   │
│  │  (flake gen) │  │  (bridge/tap)│  │  (microvm runners) │   │
│  └──────┬───────┘  └──────┬───────┘  └─────────┬──────────┘   │
└─────────┼─────────────────┼────────────────────┼──────────────┘
          │                 │                    │
┌─────────▼───────┐  ┌──────▼───────┐  ┌─────────▼──────────┐
│   nix tooling   │  │ microbe-pro- │  │  qemu / cloud-hy-  │
│  nix-instantiate│  │ visiond: net-│  │  pervisor / fire-  │
│  nix build      │  │  link/nft    │  │  cracker           │
└─────────────────┘  └──────────────┘  └────────────────────┘
```

Three layers:

1. **Config layer** — reads `microbe.nix`, evaluates its orchestration
   projection to JSON via `nix-instantiate --eval --json` (§4), validates
   against a Go schema (§6), and exposes a typed model to the rest of the CLI.
2. **Provisioning layer** — renders (a) a NixOS flake containing one `microvm`
   config per service (§8), (b) host-side network setup (bridges, taps, DNAT),
   (c) volume files (qcow2 disks) and share directories. Generated files live
   under `.microbe/` (gitignored).
3. **Runtime layer** — starts/stops the runners produced by
   `microvm.declaredRunner` for each service, tracks state (§12), streams logs.

---

# Part A — Formal specification

## 4. Dual Evaluation Model

The compose file contains values that **cannot** cross a JSON boundary
(NixOS module functions, `import` paths referencing `pkgs`). The design
therefore gives the file **two evaluation contexts**. This is the core
architectural decision of the project.

```
                          microbe.nix
                          ┌──────────────────────┐
                          │  networks            │
                          │  services.<n>.config │ ← NixOS modules (functions, paths)
                          │  services.<n>.vcpu   │
                          │  services.<n>.volumes│
                          │  ...                 │
                          └──────────┬───────────┘
                                     │
              ┌──────────────────────┴──────────────────────┐
              │                                             │
   ORCHESTRATION VIEW                                RENDER VIEW
   (JSON wire schema, §5)                         (native Nix, §4.x)
              │                                             │
   nix-instantiate --eval --json                  import ./microbe.nix
   with `config` PROJECTED OUT                    inside the generated flake,
   (existence only: configPresent)                so guest modules stay alive
              │                                             │
              ▼                                             ▼
   ┌──────────────────────┐                  ┌──────────────────────────┐
   │  Go CLI:             │                  │  generated stack flake:  │
   │  · validate          │                  │  nixosConfigurations.<svc>│
   │  · allocate IPs/MACs │── generated.nix ─▶│  imports microbe.nix│
   │  · build host nets   │  (bridge, §8.2)   │  + renderer + guest-base │
   │  · manage lifecycle  │                  └──────────────────────────┘
   └──────────────────────┘
```

### 4.1 Rules

**R1 — File form.** `microbe.nix` is either a plain attribute set or a
function `{ lib, ... } -> attribute set`. The CLI applies it with a pinned
`lib` (from nixpkgs) when it is a function.

**R2 — Projection.** In the orchestration view, every `services.<n>.config`
value is replaced by the boolean `configPresent`. All other fields must be
JSON-serializable. Evaluation of the projection must not force `config`.

**R3 — No double force.** The orchestration projection evaluates the whole
file, so top-level values other than `config` must be finite data. User code
runs only inside `config` values, which are never forced in this context.

**R4 — Native consumption.** In the render view the file is `import`ed as a
NixOS module input. `config` values are passed to the module system
unmodified, so they may be inline lambdas, `import`ed paths, or any module
value the guest accepts.

**R5 — Communication.** The only data the CLI passes back into Nix is
`.microbe/generated.nix` (§8.2): per-service MACs, CIDs, IPs, gateway,
hosts table, and rendered networkd units, written as a plain attrset of
JSON-safe values. No rendered logic flows through JSON.

---

## 5. Wire Schema (orchestration view)

The orchestration projection, formally:

> **IPv6 flat-network model**: every service in every stack on a host shares
> one flat address space -- a single ULA `/64` generated once per host
> (`internal/state.HostState`, RFC 4193, `fd` + 40 random bits) and persisted
> at `/var/lib/microbe/host-state.json`. `networks.<n>` no longer carries a
> `subnet`; it is a pure label used for grouping/`internal` egress policy and
> as `rules:` vocabulary (§8.6). A service gets exactly one IPv6 address,
> shared across every network it attaches to, generated once via
> `crypto/rand` and permanently recorded in a committed lockfile
> (`microbe.lock.json`, next to the compose file, `internal/lockfile.Lock`)
> so it never changes again -- not re-derived per `up` the way the old
> per-network IPv4 allocation was. Reachability between services is
> default-deny, opened only by the new top-level `rules:` list.

```abnf
compose       = "{" "schemaVersion" ":" version
                "name" ":" string
                "networks" ":" networks
                "services" ":" services
                [ "rules" ":" "[" *( rule ) "]" ] "}"

version       = %x31                 ; literal 1

networks      = "{" *( name ":" network ) "}"
network       = "{" [ "internal" ":" boolean ] "}"

services      = "{" *( name ":" service ) "}"
service       = "{" [ "vcpu" ":" integer ]
                    [ "mem" ":" integer ]
                    [ "hypervisor" ":" hypervisor ]
                    [ "configPresent" ":" boolean ]
                    [ "volumes" ":" "[" *( volume ) "]" ]
                    [ "networks" ":" "[" *( attach ) "]" ]
                    [ "ports" ":" "[" *( port-map ) "]" ]
                    [ "dependsOn" ":" "[" *( name ) "]" ]
                    [ "healthcheck" ":" healthcheck ] "}"

hypervisor    = "cloud-hypervisor" / "qemu" / "firecracker"

volume        = disk / share
disk          = "{" "type" ":" "disk"
                    "name" ":" string
                    "target" ":" string
                    "size" ":" size
                    [ "fsType" ":" string ] "}"
share         = "{" "type" ":" "share"
                    "host" ":" string
                    "target" ":" string
                    [ "mode" ":" ( "ro" / "rw" ) ]
                    [ "protocol" ":" ( "9p" / "virtiofs" ) ] "}"

attach        = "{" "name" ":" name [ "addr" ":" ip6-addr ] "}"

port-map      = ip-or-empty ":" port ":" port     ; "8080:80"
              / ip-or-empty ":" port ":" port "/" proto

rule          = "{" "from" ":" name "to" ":" name
                    [ "ports" ":" "[" *( port ) "]" ]
                    [ "proto" ":" ( "tcp" / "udp" ) ] "}"

healthcheck   = "{" [ "interval" ":" duration ]
                    [ "timeout" ":" duration ]
                    [ "startPeriod" ":" duration ]
                    [ "command" ":" "[" *( string ) "]" ] "}"

ip6-addr      = 1*4HEXDIG *7( ":" 1*4HEXDIG ) / "::" ...   ; RFC 4291, must fall within the host's ULA /64
size          = integer *( "K" / "M" / "G" / "T" )   ; "20G", "512M"
duration      = integer *( "ms" / "s" / "m" )
name          = 1*( ALPHA / DIGIT / "-" / "_" )
```

### 5.1 Field defaults (applied by the CLI when absent)

| Field | Default | Notes |
|-------|---------|-------|
| `schemaVersion` | `1` | Reject files with unknown versions. |
| `services.<n>.vcpu` | `1` | |
| `services.<n>.mem` | `512` | MiB. |
| `services.<n>.hypervisor` | `cloud-hypervisor` | Per-service override allowed. |
| `services.<n>.volumes` | `[]` | |
| `services.<n>.networks` | `[]` | A service may have no network. |
| `services.<n>.ports` | `[]` | Requires ≥1 network. |
| `services.<n>.dependsOn` | `[]` | |
| `services.<n>.healthcheck` | absent | Absent ⇒ no readiness gate. |
| `rules` | `[]` | Absent ⇒ no service can reach any other (default-deny). |
| `rule.proto` | `tcp` | |
| `rule.ports` | `[]` | Empty ⇒ every port for `proto`. |
| `volume.share.mode` | `rw` | |
| `volume.share.protocol` | `9p` | Matches microvm.nix default. |
| `volume.disk.fsType` | `ext4` | |
| `healthcheck.interval` | `5s` | |
| `healthcheck.timeout` | `2s` | |
| `healthcheck.startPeriod` | `10s` | |

---

## 6. Nix DSL Surface

The user-facing file may use sugar that the orchestration projection
normalizes:

| Surface | Normalizes to |
|---------|---------------|
| `services.<n>.networks = [ "frontend" "backend" ]` (list of strings) | `networks = [{ name = "frontend"; } { name = "backend"; }]` |
| `services.<n>.config = ./services/web.nix` (path) | `configPresent = true` in projection; path kept in render view. |
| `services.<n>.config = { pkgs, ... }: { ... }` (inline lambda) | `configPresent = true`; never serialized. |
| Omitted `addr` in an attach | CLI generates a random IPv6 `/128` within the host's ULA `/64`, permanently recorded in `microbe.lock.json` on first `up` (§9.4). |
| `networks.<n>` value may be `{ internal = true; }` | unchanged; no more `subnet` field. |

Forbidden in the file (validation errors):
- Top-level keys other than `name`, `networks`, `services`, `schemaVersion`.
- Non-string keys; empty `name`; reserved `name` values (`host`, `up`, etc.).
- A service with `ports` but no `networks`.
- Two services with the same `hostname` (implicit service name) on a shared
  network.

---

## 7. Validation Invariants

Enforced by the CLI after projecting to JSON; any failure ⇒ exit code 2.

| ID | Rule |
|----|------|
| V1 | `name` matches `[a-z][a-z0-9_-]{0,31}`. |
| V2 | Service names match `[a-z][a-z0-9_-]{0,31}` and are unique. |
| V3 | Network names match V2 rules and are unique. |
| V4 | Every `attach.name` references a declared network. |
| V5 | Every static `attach.addr` is a valid IPv6 address (`.Is6()`). |
| V6 | A service's attachments must agree on `addr`: setting a different static address on two of the same service's attachments is a conflict. |
| V7 | Static addresses are unique across the whole stack (one flat address space, not one per network). |
| V16 | Every `rule.from`/`rule.to` references a declared service; `from != to`. |
| V17 | No duplicate `(from, to, proto, port)` rule tuples. |
| V18 | `rule.proto` ∈ {`""`, `tcp`, `udp`}; each `rule.ports` entry in 1–65535. |
| V8 | `ports`: `hostPort` unique across the stack; values in 1–65535; `guestPort` in 1–65535. |
| V9 | `dependsOn` references existing services; the graph is acyclic. |
| V10 | Volume names unique per service; `disk.name` unique per stack. |
| V11 | `disk.target` and `share.target` are absolute paths; no two volumes in a service share a `target`. |
| V12 | `hypervisor` ∈ {`cloud-hypervisor`, `qemu`, `firecracker`}; hypervisor supported on host arch. |
| V13 | Total `mem` across services ≤ host RAM budget (configurable; warning, not hard error, by default). |
| V14 | `dependsOn` cycles and self-dependency rejected (V9). |
| V15 | `share.host` resolves relative to the compose file's directory. |

---

## 8. Integration Mapping (compose → microvm.nix)

Each service is rendered into a NixOS configuration. The mapping below is
authoritative; option names come from microvm.nix `microvm` options
(verified against `microvm-nix.github.io/microvm.nix`).

### 8.1 Identity & resources

| Compose | Rendered microvm option |
|---------|--------------------------|
| `services.<n>` | `networking.hostName = "<n>"` |
| `services.<n>.vcpu` | `microvm.vcpu = <n>` |
| `services.<n>.mem` | `microvm.mem = <n>` |
| `services.<n>.hypervisor` | `microvm.hypervisor = <n>` |
| (CLI-assigned) | `microvm.vsock.cid = <cid>` — from `generated.nix` |

### 8.2 Interfaces

For each `attach` to network `N`:

```
microvm.interfaces += {
  type = "tap";
  id   = <tap id>;              # host tap name, from generated.nix (§9.2)
  mac  = <assigned MAC>;        # 02:00:00:00:00:xx, from generated.nix
  # tap.vhost default off (v1)
};
```

- **Tap ids are ≤15 chars** (Linux `IFNAMSIZ`). The CLI computes
  `mvc-<stack>-<svc>-<N>` when it fits, else a deterministic
  `mvc-` + 11 hex chars of `sha256(stack-svc-net)`. The renderer reads the id
  from `gen.taps` so the host side (CLI provisioning) and guest side agree.

- **Host side** (provisiond, not Nix): one bridge per **stack** (not per
  network -- `br-<stack>`, `hostnet.BridgeName`), shared by every network the
  stack declares; tap `mvc-...` enslaved; bridge address is the host's one
  flat-network gateway (`<host ULA prefix>::1`, the same address on every
  stack's bridge -- see the known multi-stack gap noted in §8.6).
- Taps are **not** auto-created by the hypervisor; `microbe-provisiond`
  creates them via netlink (§13.1) because microvm.nix only manages them
  under the `host` module, which we do not require.

### 8.3 Networking inside the guest

The CLI generates a networkd unit as data into `generated.nix`; the renderer
merges it (no hand-written per-VM networking):

```
systemd.network.networks."mvc-<svc>-<N>" = {
  matchConfig.MACAddress  = <mac>;
  linkConfig.RequiredForOnline = "no";
  address = [ "<addr>/64" ];        # same addr on every one of this service's units
  routes  = [ { Gateway = "<gateway>"; } ];   # only on the first-declared network's unit
};
```

- A service has exactly **one** IPv6 address, shared across every network
  attachment (not one per network the way the old per-network IPv4 model
  worked) -- every `systemd.network.networks` unit for the service carries
  the same `address`. Only the *first* declared network's unit gets a
  default route (`routes = [{ Gateway = ...; }]`); every other attachment's
  unit gets `routes = []` (must marshal to a JSON empty array, not `null` --
  `systemd-networkd` rejects `null` for this option), since the whole flat
  `/64` is reachable via the shared gateway regardless of which tap traffic
  entered on.
- `networking.useDHCP = false`; DNS = the host's dns64 resolver address
  (`gen.gateway` -- see §13.1's NAT64/DNS64 module), not a hardcoded literal:
  guests are IPv6-only, so an IPv4 literal like `1.1.1.1` is unreachable.
- `/etc/hosts`: one entry per service, `<addr> <svcname>` -- no more
  `<svcname>.<network>` alias, since every attachment resolves to the same
  address now, adding no information a second entry would carry.

### 8.4 Volumes — `disk`

```
microvm.volumes += {
  image      = "<stack>/<name>.qcow2";   # under the CLI-managed volume dir
  mountPoint = <target>;
  size       = <MiB(size)>;              # "20G" → 20480
  autoCreate = true;                     # CLI pre-creates; belt-and-suspenders
  fsType     = <fsType or "ext4">;
};
```

- The CLI ensures the image exists (`qemu-img create`) before start.
- `mountPoint` registration lives in microvm.nix; no extra `fileSystems`
  needed by the user.
- `down` keeps images; `rm` deletes them.

### 8.5 Volumes — `share`

```
microvm.shares += {
  proto      = <protocol or "9p">;
  tag        = "mvc-<stack>-<svc>-<n>";   # unique per share
  source     = <absolute host path>;      # resolved against compose dir
  mountPoint = <target>;
  readOnly   = <mode == "ro">;
};
```

- `source` may be absolute or relative to `/var/lib/microvms/$hostName`;
  the CLI resolves relative `host` to an absolute path under `.microbe/shares/`.
- `virtiofs` requires the virtiofsd socket wiring; the renderer emits the
  per-share `socket` path under `.microbe/sockets/` and the CLI starts
  virtiofsd alongside the runner (mirrors `microvm-virtiofsd@.service`).

### 8.6 Networks (orchestration, host side)

| Compose | Host resource | Lifecycle |
|---------|---------------|-----------|
| any network attachment in the stack | one bridge `br-<stack>` @ the host's flat-network gateway | created `up`, never deleted by `down` (see below) |
| each attach | tap `mvc-<stack>-<svc>-<N>` | created `up`, removed `down` |
| `ports` | nftables DNAT (IPv6 family) `<hostPort> → [<guest addr>]:<guestPort>`, straight to the guest's real address -- no NAT64 needed for an IPv6-capable external client | created `up`, removed `down` |
| `rules` | nftables `forward` chain (IPv6 family, table `microbe`): `Policy: drop` + one `ct state established,related accept` + one accept per rule, matching exact service addresses (no subnet/mask, since these are single `/128`s) | (re)applied `up`, removed `down` |

- **Known gap**: every stack's bridge gets assigned the *same* host-wide
  gateway address (`<host ULA prefix>::1`), since every service shares one
  flat `/64` and there's currently no per-stack address carve-out. This is
  correct for the common single-stack-per-host case but means two stacks
  running concurrently on the same host will collide on that address --
  tracked as a follow-up (candidate fix: collapse to one truly host-global
  bridge instead of one per stack, since the `rules:` forward-chain
  default-deny already provides the actual cross-service isolation boundary
  now, not bridge topology).
- **Provisioning record**: `up` appends the created bridge/taps to
  `state.json`'s `provisioned` field (`json:"provisioned"`). `down` also
  *culls orphaned links*: after tearing down a selected service, any
  recorded device neither in the current config nor on a service staying up
  is deleted (best-effort, exact-name) via the daemon's `teardown_links`
  RPC, and swept names drop out of `provisioned`. This cleans leftovers from
  earlier aborted runs. The bridge itself is treated as a permanent,
  idempotently-(re)ensured host fixture -- `down`/`purge` never delete it
  (mirrors `docker0` never being torn down by `docker stop`), only taps.
- **Address allocation**: a service's one IPv6 address is generated once via
  `crypto/rand` (a random `/128` within the host's ULA `/64`) and
  permanently recorded in the stack's committed `microbe.lock.json`
  (`internal/lockfile.Lock`) -- not re-derived per `up` the way the old
  per-network "next free host IPv4" scan was. A static `attach.addr`
  overrides random generation and is written into the lockfile too.
- **MAC allocation**: `02:00:00:00:00:<2-hex>`, unique per interface across
  the stack, persisted in `generated.json`/`state.json` -- unchanged by the
  IPv6 migration (MACs identify taps for the guest, addresses identify
  services; only the latter needed to change).
- **Outbound internet egress**: no more nftables masquerade (there's no
  per-network subnet left to masquerade). Guests reach the general IPv4
  internet via NAT64 (tayga) + DNS64 (unbound) on the host instead -- see
  §13.1.

### 8.6.1 `microbe purge`

Docker-style convergence/hammer command family, scoped to the current stack
unless `--all` widens it host-wide across every `/var/lib/microbe/<stack>`:

- bare `purge` (and `purge networks`/`nets`): deletes recorded bridges/taps
  that neither the *current config* names nor a **live VM** is attached to
  (live = recorded PID or an answering cloud-hypervisor API socket). State
  drops the swept names.
- `purge vms`: stops recorded PIDs (+ virtiofsd companions), then finds
  **unrecorded** VMMs — a service whose state lost its PID but whose socket
  still answers vm — by matching `/proc/*/cmdline` for `cloud-hypervisor
  --api-socket`, scoped to the invoking user.
- `purge volumes`/`vols`: removes the on-disk disk images a stack's services
  declare and clears their state (like `rm`), with a confirmation prompt.
- `purge all`: host-wide network sweep + VM purge (confirmation prompt; `-f`
  skips prompts; `--dry-run` prints without mutating).

### 8.7 Ordering & health

| Compose | Integration |
|---------|-------------|
| `dependsOn` | `up` builds a DAG; topological start. Dependency failure ⇒ dependents not started (exit 3). |
| `healthcheck` | Renderer installs a guest systemd unit (e.g. `tcpsocket` or `exec`) + CLI polls the runner/guest until healthy or timeout (`startPeriod + interval`). |

> **Implementation note (M5, verified live)**: shipped as **TCP-socket-only**
> healthchecks, not the full `exec`-in-guest kind above — that would need
> SSH key injection, an SSH client dependency, and a real `microbe exec`
> (all still unbuilt; `exec.go` is a stub). `Healthcheck.Command` was
> replaced with `Port int`: after starting a service, if it declares a
> `healthcheck`, `internal/cmd/up.go` dials `<primary-network-IP>:Port`
> (`internal/cmd/health.go`'s `waitHealthy`) every `interval` until it
> accepts a connection or `startPeriod` elapses, no guest-side unit
> involved. Gating is **fail-fast**: the single sequential start loop
> (dependency-first order from `startOrder`) breaks on the first service
> that never becomes healthy, marking it `degraded` in `state.json` and
> returning an error — so nothing declared later in the order (including
> real dependents) starts, and `ps` still shows what happened instead of
> the run vanishing silently. Verified against `db`/`web`/`jump` on lappy:
> healthy case prints `healthy db` and reaches all-`running`/`healthy`;
> pointing the healthcheck at a port nothing listens on reproduces
> `degraded db: ... within 10s`, exit 1, and `jump`/`web` staying `stopped`
> with no PID.

### 8.8 Guest base module

The renderer injects `modules/guest-base.nix` (fixed, shipped with the CLI)
so every VM has: `services.openssh` (key auth via injected pubkey, root
password login disabled by default), networkd, `systemd-networkd` enabled,
no firewall on managed networks, journald streaming socket for `logs`.

---

## 9. Rendered Artifacts

### 9.1 Layout

```
.microbe/
├── flake.nix                    ; generated per stack
├── modules/
│   ├── renderer.nix             ; fixed: compose → microvm mapping (imports generated.nix + user file)
│   ├── guest-base.nix           ; fixed: ssh, networkd, journal socket
│   └── <svc>.nix                ; per-service: injects svc config + identity
├── generated.nix                ; CLI data: macs, cids, ips, hosts, networkd units
├── volumes/<stack>/<name>.qcow2
├── shares/                      ; materialized relative share dirs
├── sockets/<tag>.sock           ; virtiofsd sockets
├── runners/<svc>                ; resolved microvm-run scripts
├── logs/<svc>.log
└── state.json
```

### 9.2 `generated.nix` shape (the CLI↔Nix bridge)

```nix
{
  services = {
    db = {
      cid = 3;
      macs = { backend = "02:00:00:00:00:02"; };
      addr = "fd7a:3c9e:1122::2";        # one address, shared by every attachment
      gateway = "fd7a:3c9e:1122::1";     # one gateway, shared by every service/stack
      prefix  = 64;                      # always 64 (the host ULA prefix length)
      hosts = [ { ip = "fd7a:3c9e:1122::2"; names = [ "db" ]; } ];  # one entry per service
      networkd = { /* exact systemd.network attrset, §8.3 */ };
      taps = { backend = "mvc-<stack>-<svc>-<net>"; };  # ≤15 chars, see §8.2
    };
  };
}
```

> **Field shape note**: `addr`/`gateway`/`prefix` are singular values now
> (one per service, one per host), not per-network maps the way
> `ips`/`gateway`/`prefix` used to be -- there's only one address and one
> gateway to describe once a service is IPv6-only on a flat network. `macs`
> and `taps` stay per-network maps (unchanged): a service still gets one tap
> per network attachment, only the *address* on those taps is now shared.

> **Implementation note (M2)**: tap ids are computed by the CLI (see §8.2) so
> the host provisioning and the guest config always agree; the renderer reads
> `gen.taps` rather than re-deriving the name.


### 9.3 Generated flake (rendered by text/template)

```nix
{
  inputs = { nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
             microvm.url = "github:microvm-nix/microvm.nix"; };
  outputs = { nixpkgs, microvm, ... }:
    let
      system = "x86_64-linux";
      compose = import ./microbe.nix;          # native render view (R4)
      mkSvc = name:
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            microvm.nixosModules.microvm
            ./modules/renderer.nix
            ./modules/guest-base.nix
            ./modules/${name}.nix
            (compose.services.${name}.config or ({ ... }: { }))  # user NixOS module
          ];
        };
    in { nixosConfigurations = builtins.mapAttrs (name: _: mkSvc name) <services>; };
}
```

> **Implementation notes (M2, verified against microvm.nix)**:
> - `builtins.mapAttrs` must pass the attr name, not the (null) value:
>   `(name: _: mkSvc name)`. The plan's original `(_: mkSvc)` passed `null`.
> - The user's compose file cannot be a NixOS module directly (`services.*`
>   clashes with NixOS options), so the flake imports it, extracts
>   `compose.services.${name}.config` as the service's NixOS module, and the
>   renderer (which imports `./microbe.nix` + `./generated.nix` itself) maps
>   the rest.


Per-service module `modules/<svc>.nix` sets `microCompose.serviceName` (an
option owned by the renderer) so the renderer picks the right slice of the
compose file and generated data.

### 9.4 `state.json` shape (CLI state)

```json
{
  "stack": "app-stack",
  "networks": ["backend"],
  "services": {
    "db": {
      "addr": "fd7a:3c9e:1122::2",
      "networks": ["backend"],
      "cid": 3,
      "macs": { "backend": "02:00:00:00:00:02" },
      "volumes": ["db-data"],
      "status": "running",
      "pid": 4242,
      "runner": ".microbe/runners/db"
    }
  },
  "ports": { "5432": { "svc": "db", "guest": 5432 } }
}
```

> `networks` is now a flat list of network *names* (used only to
> reconstruct the stack's bridge/taps without a config, e.g. for a host-wide
> `purge`), not a map with per-network CIDR/allocation data -- there's no
> more per-network subnet or allocation table once every service shares one
> flat `/64`. Each service's own `networks` list (distinct from the
> top-level one) is the set of networks *that service* is attached to,
> needed to reconstruct its taps.

### 9.5 `up` lifecycle walkthrough

1. **Load**: eval orchestration projection → JSON → validate (§5–§7).
2. **Render**: write `.microbe/` flake + modules + `generated.nix`
   (idempotent).
3. **Build**: `nix build` each service's runner (cache-friendly; unchanged
   config → store hits).
4. **Provision** (only if missing / `--no-provision` to skip): the CLI sends
   the bridge/tap/DNAT specs to the **microbe-provisiond** daemon over its
   unix socket (`/run/microbe.sock`); the root daemon applies them itself via
   netlink (bridges, taps, addresses) and the nftables netlink protocol
   (DNAT, forward-chain rules) — no `ip`/`iptables` exec, no sudo (§13.1):
   - resolve the host's ULA prefix (generate once, first `up` ever; §5's
     intro note) and the stack's committed `microbe.lock.json` (generate any
     missing service addresses, persist);
   - create/ensure the stack's one bridge `br-<stack>`, assign the host's
     shared gateway address;
   - create qcow2 disks at `volumes/<stack>/<name>.qcow2` (qemu-img, unprivileged) if absent;
   - create tap devices and attach to the bridge;
   - apply nftables DNAT for `ports` and forward-chain accept rules for
     `rules:` (default-deny otherwise).
5. **Start**: topological order from `dependsOn`; launch each
   `microvm-run` with the tap/socket args; record PID in `state.json`.
6. **Wait**: for each service, poll `healthcheck` (or wait for SSH) until ready
   or timeout; gate dependents.
7. **Report**: print table of services, IPs, published ports.

`down` reverses: stop runners (SIGTERM), remove taps/bridges/DNAT, keep
disks + state. `down --remove-volumes` also deletes qcow2 disks.

> **Implementation notes (M3/M4, verified by dry-run)**: host provisioning is
> **docker-style**: a root daemon, `microbe-provisiond`, owns all privileged
> network state and applies it itself via `vishvananda/netlink` (bridge/tap/
> address) and the `google/nftables` netlink protocol (DNAT), exactly as
> Docker's libnetwork does. It is exposed through a unix socket
> `/run/microbe.sock` owned `root:microbe` mode `0660` (systemd socket unit,
> mirroring `systemd.sockets.docker`), so members of the `microbe` group can
> drive provisioning **without any shell-level privilege** — no sudoers, no
> setuid, no capability grants. Operations are idempotent: netlink link
> lookup errors (`LinkNotFound`) trigger create; addresses are replaced; DNAT
> rules are checked before install. **Tap ownership gotcha (M3 live E2E)**:
> a tap that already exists is reused as-is, so a stale tap created by an older
> binary with a wrong owner pins the tap to root and the unprivileged
> cloud-hypervisor process (the `microbe`-group user) gets `EPERM` on attach.
> `EnsureTaps` must therefore reconcile ownership (delete + recreate a tap whose
> sysfs `owner`/`group` differs from the spec), not just check existence.
> Teardown is best-effort (delete errors ignored). The CLI builds bridge/tap/DNAT specs from `flakegen.Stack` (same
> `TapID` source as the renderer), gated behind package-level seams
> (`provisionHost` / `teardownHost`) so the cmd layer is testable without
> root or a daemon. Runners launch as detached processes (Setpgid + Release)
> with CWD `.microbe/runs/<svc>` (the runner script drops `microvm.sock` in
> CWD) and log to `.microbe/logs/<svc>.log`. Volume qcow2 images use
> `qemu-img create -f qcow2 -o size=<MiB>M` only when absent, at
> `.microbe/volumes/<stack>/<name>.qcow2`. State is written atomically
> (temp+rename) to `.microbe/state.json` with the §9.4 shape. The daemon
> requires root; the CLI never does. `--dry-run` prints the intended
> provisioning actions and never contacts the daemon or starts anything.
>
> **Git-resolution gotcha (M3/M4)**: `.microbe/` is gitignored, but a flake
> path input inside a git repo is resolved via git, so a direct `nix build`
> in `.microbe/` fails ("does not contain a flake.nix") — git excludes the
> ignored directory from the tree. `nix.BuildRunner` therefore stages the
> flake sources (flake.nix, microbe.nix, generated.nix, modules/) to a temp
> dir outside the work tree, builds there, and out-links back to
> `.microbe/runners/<svc>`. Runtime artifacts (volumes/, runs/, logs/,
> state.json) are never staged. Verified: staged tree evals cleanly; a full
> `nix build` realizes the NixOS+hypervisor closure (minutes, warm-store
> dependent), so cheap `nix eval` is the CI-grade check.

---

# Part B — Project plan

## 10. CLI Surface

```
microbe [global flags] <command>

Commands:
  up        [--build] [--no-provision] [services...]
            Build + provision + start the stack (or listed services).
  down      [--remove-volumes] [services...]
            Stop and remove runners. Volumes persist unless --remove-volumes.
  ps        [-a]              List services, status, IPs, ports.
  logs      [--follow] [-n N] [services...]
            Follow/tail guest journald or runner stdout.
  exec      <service> <cmd...>
            Run a command inside a service (ssh over vsock / console).
  restart   [services...]     Stop + start.
  config    Print the evaluated+validated JSON config.
  build     Build runner derivations without starting.
  rm        [-f] [services...]  Remove disks + state for listed services.
  version

Global flags:
  -f, --file PATH   Compose file (default ./microbe.nix)
  -v, --verbose     Verbose output
  --dry-run         Print what would happen without doing it
```

Exit codes: `0` success, `1` operational error, `2` config/validation error,
`3` dependency-start failure, `4` missing prerequisite (no `nix`, daemon
unreachable for provisioning, host arch mismatch).

---

## 11. Go Project Layout

```
microbe/
├── go.mod                     # module microbe; Go 1.22+
├── main.go                    # entry: cobra root command
├── internal/
│   ├── config/
│   │   ├── load.go            # eval nix→JSON, read+validate
│   │   ├── schema.go          # typed structs matching §5
│   │   ├── eval.go            # nix-instantiate wrapper + projection
│   │   └── validate_test.go
│   ├── nix/                   # nix tooling interop
│   │   ├── instantiate.go
│   │   ├── build.go           # nix build for runner derivations
│   │   └── flakegen/          # Go text/template emitting the stack flake
│   │       ├── flake.nix.tmpl # §9.3
│   │       ├── generated.nix.tmpl # §9.2
│   │       ├── renderer.nix   # fixed mapping module (§8)
│   │       ├── guest-base.nix # fixed (§8.8)
│   │       └── render.go
│   ├── state/
│   │   ├── store.go           # state.json (§9.4)
│   │   └── store_test.go
│   ├── hostnet/
│   │   ├── plan.go             # IP/MAC allocation (§8.6), spec derivation
│   │   ├── spec.go             # NetSpec / TapSpec / PortSpec, BridgeName
│   │   ├── plan_test.go
│   │   └── spec_test.go
│   ├── provisiond/             # root daemon (docker-style, §13.1)
│   │   ├── protocol.go         # request/response types over the socket
│   │   ├── netops.go           # netlink bridge/tap/addr (§8.6)
│   │   ├── nft.go              # nftables DNAT for port publishing
│   │   ├── server.go           # unix socket listener, dispatch
│   │   ├── client.go           # CLI-side client (dial, call)
│   │   ├── server_test.go
│   │   └── client_test.go
│   ├── runtime/
│   │   ├── runner.go          # discover/exec microvm-run scripts
│   │   ├── up.go, down.go, ps.go, logs.go, exec.go
│   │   ├── journal.go         # guest journald streaming via vsock
│   │   └── health.go          # healthcheck probe loop
│   ├── dockercompat/          # (v2) docker-compose file adapter
│   └── cmd/
│       ├── root.go            # cobra wiring
│       ├── up.go, down.go, ps.go, logs.go, exec.go, config.go
│       └── common.go          # flags, context, spinner
└── test/
    ├── integration/           # real-VM smoke tests (tag: integration)
    └── fixtures/              # sample microbe.nix files
```

Dependencies (keep minimal):
- `github.com/spf13/cobra` — CLI framework.
- `github.com/vishvananda/netlink` — bridge/tap/address setup (pure Go, no `ip`).
- `github.com/google/nftables` — nftables DNAT via netlink (no `iptables`).
- `golang.org/x/sys` — transitive netlink dependency.
- `gopkg.in/yaml.v3` only if/when a docker-compose adapter lands.

---

## 12. Lifecycle State Machine

```
                     +--------+     down      +--------+
        up ───────▶  │ START  │ ───────────▶  │ STOPPED│ ◀────── up
                     +--------+               +--------+
                         │                        ▲
                 provisioned                  │
                         ▼                        │
                     +--------+     boot fail /  │ down
                 ┌──▶│ RUNNING│ ── stop ────────┘
                 │   +--------+
      unhealthy  │       │ healthy
                 │       ▼
                 │   +--------+
                 └──▶│ HEALTHY│
                     +--------+
```

| State | Meaning |
|-------|---------|
| `stopped` | Runner not running; volumes/state retained. |
| `starting` | Runner launched; waiting on readiness. |
| `running` | Process alive; no healthcheck or within start period. |
| `healthy` | Healthcheck passing. |
| `degraded` | Healthcheck failing after grace. |
| `provisioned` | Build done, host nets + disks ready, not yet started. |

---

## 13. Security & Safety

- Bridges/taps/DNAT require root, but **only the `microbe-provisiond` daemon
  holds that privilege** (§13.1). The CLI runs unprivileged and reaches the
  daemon through a `root:microbe` mode-`0660` unix socket. Members of the
  `microbe` group get network administration for declared stacks — the same
  trust model as the `docker` group — but never gain shell-level
  privilege: no sudoers rules, no setuid, no capabilities. If the daemon is
  unreachable or missing, the CLI fails fast with a clear message.

### 13.1 Host NixOS module (`modules/host.nix`)

A host that runs microbe needs kernel/networking readiness, a root
provisioning daemon, and device access. The flake ships a NixOS module,
`nixosModules.host` (option namespace `virtualisation.microbe`, modeled after
`virtualisation/docker.nix`):

- **Kernel modules**: `tun`, `br_netfilter`, `vhost`, `vhost_net`, and
  `kvm_intel`/`kvm_amd` (modprobe warns harmlessly when the CPU lacks the
  feature).
- **Sysctls** (priority 98, so user config can override): `net.ipv4.ip_forward`
  + per-interface forwarding, `net.ipv6.conf.{all,default}.forwarding` (guests
  are IPv6-only now -- the host must forward between stack bridges and, with
  `nat64.enable`, into the tayga tun device, regardless of whether NAT64
  itself is on), and `net.bridge.bridge-nf-call-{ip,ip6}tables` so the
  nftables DNAT that publishes VM ports sees bridged traffic.
- **NAT64/DNS64** (`virtualisation.microbe.nat64.enable`, default on):
  `services.tayga` gives guests outbound-only NAT64 to the general IPv4
  internet (well-known `64:ff9b::/96` prefix, configurable `ipv4Pool` --
  RFC 6598 CGNAT space by default); `services.unbound` runs as a DNS64
  resolver (rewrites A-only answers into the NAT64 prefix), `access-control`
  hardcoded to `fd00::/8` since every host ULA prefix microbe generates
  falls under that range. Reachability *to* a published port needs no
  NAT64 for an IPv6-capable client (direct DNAT, §8.6); IPv4-only external
  clients aren't supported yet (tayga's static `mappings` render once at
  nix-eval time, incompatible with ports published/unpublished at ordinary
  `up`/`down` time).
- **Packages**: `qemu-utils` (`qemu-img` for VM volume images), plus the
  `microbe` CLI when `virtualisation.microbe.package` is set (the flake sets it
  to `packages.<system>.microbe`). No `ip`/`iptables` userspace is required:
  the daemon does netlink itself.
- **Provisioning daemon** (docker-style): a `systemd.sockets.microbe-provisiond`
  unit owns `/run/microbe.sock` (`SocketUser=root`, `SocketGroup=microbe`,
  `SocketMode=0660`), and a `systemd.services.microbe-provisiond` root unit
  runs `microbe provisiond` (socket-activated) which applies bridge/tap/DNAT
  ops via netlink. Mirrors `systemd.sockets.docker` / `dockerd`.
- **Device access**: `microbe` and `kvm` groups; udev rules granting them
  `/dev/net/tun` and `/dev/kvm`; `virtualisation.microbe.users` adds named
  users to both groups. Device nodes are the only host resources the group
  touches directly; all network provisioning goes through the daemon socket.
- The module is inert unless `virtualisation.microbe.enable = true`. The
  ISO target in the repo flake enables it.

The CLI package is built with `pkgs.buildGoModule` (`vendorHash = null`) from
the vendored `./vendor` directory, so it builds offline.

- All state/config paths stay inside the project dir; no writes outside
  `.microbe/` except host network changes.
- Passwords/secrets: guest configs should use `users.users.<u>.hashedPassword`
  or keys; CLI never injects plaintext passwords into rendered files.
- The generated guest modules must **not** allow the VM to escape to host
  networking beyond its bridge: no promiscuous taps, DNAT only for declared
  ports, and default-deny service-to-service reachability enforced by the
  `rules:` list (§8.6) -- shipped, not a future v2 item.
- `nix-instantiate --eval` runs user config — document that the compose file
  is trusted input (same trust model as the user's own nix config).
- Guest base module disables root password login over SSH by default (§8.8).

---

## 14. Error Handling & UX

- Config errors: report with Nix line info where possible; exit code 2.
- Build errors: surface the `nix build` failure excerpt (last N lines) with the
  service name; exit code 1.
- Runtime errors: include service name, PID, and last log tail; suggest
  `microbe logs <svc>`.
- All commands are `--dry-run`-able and idempotent where sensible.
- `ps` shows: service, status (starting/running/healthy/degraded/stopped),
  UPTIME, IPs, ports.
- Colors only when stdout is a TTY; JSON output via `--output json` for
  scripting.

---

## 15. Testing Strategy

Unit tests (Go):
- `config/validate_test.go` — schema validation edge cases (V1–V15: bad CIDR,
  missing volume name, unknown network ref, duplicate IPs, dependsOn cycles).
- `hostnet/ipalloc_test.go` — allocation, reuse, exhaustion, conflicts.
- `nix/flakegen` — golden-file tests for rendered flake/modules against
  fixtures.
- `state/store_test.go` — read/write/merge/corrupt-file handling.

Integration tests (tagged `//go:build integration`, opt-in):
- Real `up`/`down` cycle with 2 VMs (db + web) on a shared bridge; assert
  cross-VM reachability, port publish, volume persistence across
  `down`/`up`.
- Healthcheck gating: web does not start until db healthy.
- Requires a NixOS host with microvm tooling; run in CI via a nix devshell.

Nix-side checks: `nix flake check` on a generated stack fixture to ensure the
rendered modules build.

---

## 16. Milestones

| Milestone | Scope | Exit criteria |
|-----------|-------|---------------|
| **M1 — Config & eval** | schema structs (§5), projection eval (§4), validation (§7), `config` command | `microbe config` prints validated JSON for the sample file. |
| **M2 — Render & build** | flake/module templates (§9), `build` command | `microbe build` produces runner derivations for db+web. |
| **M3 — Host net** | bridges, taps, IP alloc, DNAT (§8.6) | Manual `ip link` inspection shows bridge+taps; ports reachable. |
| **M4 — Lifecycle** | `up`/`down`/`ps` | Single-VM `up`/`down` works end-to-end; volumes persist. || **M5 — Multi-VM** | dependsOn, health, name resolution (§8.7) | db+web stack up; `web` reaches `db` by hostname; web gated on db health. |
| **M6 — Observability** | `logs`, `exec`, `restart`, `rm` | All commands work against the sample stack. |
| **M7 — Hardening** | error UX, dry-run, json output, docs | Full sample stack lifecycle passes integration tests. |

---

## 17. Open Questions

1. **exec transport**: ssh-over-vsock requires a guest agent + key injection;
   simpler v1 is `exec` via `cloud-hypervisor` console or `firecracker` API.
   Decide: is a persistent sshd in every guest acceptable? (Affects §8.8.)
2. **Bridge management**: create bridges imperatively via netlink at `up` time,
   or declare them in host NixOS (systemd-networkd) and have the CLI just
   attach? Current leaning: imperative + `--dry-run`, with a future
   `--host-config` command that emits the NixOS module to make them static.
3. **Port publish for cloud-hypervisor**: DNAT vs socat proxy on bridged taps.
   Requires a spike; DNAT is the spec default (§8.6).
4. **`microvm.volumes.image` location**: confirm whether microvm.nix resolves
   `image` relative to `/var/lib/microvms/$hostName` only, or accepts absolute
   paths. If relative-only, the CLI pins the volume dir per service (§8.4).
5. **`dependsOn` network inheritance**: should dependents *implicitly* join
   all networks of their dependency, or only share `/etc/hosts`? Current spec:
   hosts only; explicit networks still required (§8.7).
6. **Nix eval performance**: `nix-instantiate` on large configs per command
   run; consider caching the evaluated JSON keyed by file hash.
7. **docker-compose adapter**: parse an existing `docker-compose.yml` into a
   `microbe.nix` (mapping images→nixos modules is lossy). Best-effort
   converter in v2?
8. **`mem`/`vcpu` defaults and overcommit**: default the same as microvm.nix
   (no overcommit) vs docker-style overcommit (all VMs declared, host may be
   smaller). Leaning: explicit is better; document required host RAM (V13).

---

## 18. References

- microvm.nix options: <https://microvm-nix.github.io/microvm.nix/microvm-options.html>
- microvm.nix shares: <https://microvm-nix.github.io/microvm.nix/shares.html>
- microvm.nix interfaces: <https://microvm-nix.github.io/microvm.nix/interfaces.html>
- microvm.nix volumes: <https://microvm-nix.github.io/microvm.nix/volumes.html>
- microvm.nix conventions (runner/host contract): <https://microvm-nix.github.io/microvm.nix/conventions.html>
- microvm.nix: <https://github.com/microvm-nix/microvm.nix>
- Current repo microvm config: `microvm-config.nix` (tap `unc0`, static
  `192.168.99.2/24`, MAC-matched networkd).
- Existing flake structure to mirror for rendering: `flake.nix` in this repo.
