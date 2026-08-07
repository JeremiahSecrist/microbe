# micro-compose — Docker-Compose-Style Orchestration for microvm.nix

Status: **Draft for review**
Date: 2026-08-07

---

## 1. Overview & Motivation

`microvm.nix` makes it easy to declare and run a single MicroVM on NixOS.
What is missing today is *orchestration of multiple VMs as a unit*: shared
virtual networks, persistent volumes that survive reboots, startup ordering,
port publishing, and a stack-level lifecycle (`up` / `down` / `ps` / `logs`).

This project adds a **Go CLI** named `micro-compose` that treats microvm.nix
the way docker-compose treats Docker: one declarative file (`micro-compose.nix`)
describes a stack of VMs, and the CLI renders, provisions, and manages them.

Key design decision: **the config file is written in Nix**, not YAML/JSON.
Nix is already the language of this stack and gives us module reuse, functions,
and build-time guarantees. The Go CLI does **not** parse Nix itself; it shells
out to `nix-instantiate --eval --json` to lower the config to JSON, then
orchestrates from that.

---

## 2. Goals

| # | Goal |
|---|------|
| G1 | Declare an entire multi-VM stack in one `micro-compose.nix` file. |
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
│                       micro-compose <cmd>                     │
└───────────────────────────┬──────────────────────────────────┘
                            │
┌───────────────────────────▼──────────────────────────────────┐
│                        Go CLI  (micro-compose)                │
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
│   nix tooling   │  │   ip / ipt    │  │  qemu / cloud-hy-  │
│  nix-instantiate│  │  (or netlink) │  │  pervisor / fire-  │
│  nix build      │  │              │  │  cracker           │
└─────────────────┘  └──────────────┘  └────────────────────┘
```

Three layers:

1. **Config layer** — reads `micro-compose.nix`, evaluates it to JSON via
   `nix-instantiate --eval --json`, validates against a Go schema, and exposes
   a typed model to the rest of the CLI.
2. **Provisioning layer** — generates (a) a NixOS flake containing one
   `microvm` config per service, (b) host-side network setup (bridges, taps),
   (c) volume files (qcow2 disks) and share directories. Puts the generated
   files in the project's `.micro-compose/` dir (gitignored).
3. **Runtime layer** — starts/stops the runners produced by
   `microvm.declaredRunner` for each service, tracks state, streams logs.

---

## 4. The Config Schema (`micro-compose.nix`)

The file is a Nix attribute set with three top-level keys: `name`, `networks`,
and `services`. Full sample below (same one presented for review, kept as the
canonical example).

```nix
# micro-compose.nix
{
  name = "app-stack";

  # Virtual networks. Each becomes a host-side bridge + per-VM taps.
  # `subnet` is required and must be a /24 or smaller that doesn't collide
  # with the host's networks.
  networks = {
    frontend = { subnet = "192.168.50.0/24"; };
    backend  = { subnet = "192.168.51.0/24"; };
  };

  services = {
    db = {
      vcpu = 2;
      mem  = 1024;

      config = { pkgs, ... }: {
        services.postgresql = {
          enable = true;
          package = pkgs.postgresql_16;
        };
      };

      volumes = [
        # Named block device: CLI-managed qcow2, survives down/up.
        { type = "disk";  name = "db-data"; target = "/var/lib/postgresql"; size = "20G"; }
        # Host path share: virtiofs/9p mount into guest.
        { type = "share"; host = "./backups"; target = "/backups"; mode = "rw"; }
      ];

      networks = [
        { name = "backend"; ip = "192.168.51.2"; }
      ];

      ports = [ "5432:5432" ];

      healthcheck = { interval = "5s"; timeout = "2s"; };
    };

    web = {
      vcpu = 2;
      mem  = 2048;

      config = ./services/web.nix;   # or a module path

      dependsOn = [ "db" ];

      networks = [
        { name = "backend";  ip = "192.168.51.3"; }
        { name = "frontend"; ip = "192.168.50.3"; }
      ];

      ports = [ "8080:80" ];
    };

    jump = {
      config = { ... }: { services.openssh.enable = true; };
      networks = [ "frontend" "backend" ];   # shorthand: auto IPs
    };
  };
}
```

### 4.1 Field reference

**Top level**

| Field      | Type       | Required | Meaning |
|------------|------------|----------|---------|
| `name`     | string     | yes      | Stack name; prefixes runners, bridges, disks, state. |
| `networks` | attrset    | yes      | Named networks. |
| `services` | attrset    | yes      | Named services (VMs). |

**`networks.<name>`**

| Field    | Type   | Required | Meaning |
|----------|--------|----------|---------|
| `subnet` | string | yes      | CIDR, e.g. `192.168.50.0/24`. Must not overlap host LAN or other networks. |

**`services.<name>`**

| Field         | Type          | Required | Meaning |
|---------------|---------------|----------|---------|
| `vcpu`        | int           | no       | vCPUs (default 1). |
| `mem`         | int           | no       | RAM in MiB (default 512). |
| `hypervisor`  | enum          | no       | `cloud-hypervisor` (default) / `qemu` / `firecracker`. |
| `config`      | nixos module  | yes      | The guest NixOS module — the "image". |
| `volumes`     | list          | no       | Disks and shares (see §4.2). |
| `networks`    | list of attrs | no       | Attach points (see §4.3). |
| `ports`       | list of str   | no       | `hostPort:guestPort` publishes. |
| `dependsOn`   | list of str   | no       | Start ordering. |
| `healthcheck` | attr          | no       | Readiness probe for `up` gating. |
| `cpuset` / `memoryBones` | ... | no | Pass-through to microvm.nix pinning (v2). |

**`services.<name>.volumes[]`**

| Field | Type | Required | Kind: `disk` (block device) | Kind: `share` (host path) |
|-------|------|----------|-----------------------------|---------------------------|
| `type`    | enum | yes      | `disk` | `share` |
| `name`    | str  | disk yes | CLI-managed qcow2 volume name | n/a |
| `target`  | str  | yes      | Mount point in guest (via filesystems). | Mount point in guest. |
| `size`    | str  | disk yes | e.g. `20G`. | n/a |
| `host`    | str  | share yes | n/a | Host path (relative to compose file). |
| `mode`    | str  | share no  | n/a | `ro`/`rw` (default `rw`). |
| `protocol`| enum | no       | n/a | `virtiofs` (default) / `9p`. |

**`services.<name>.networks[]`**

| Field | Type | Required | Meaning |
|-------|------|----------|---------|
| `name` | str  | yes      | Which network (must exist in `networks`). |
| `ip`   | str  | no       | Static IP. Omitted → auto-assign next free host in subnet. |

If the list element is a bare string, it is shorthand for `{ name = str; }`.

**`services.<name>.healthcheck`**

| Field      | Type | Required | Meaning |
|------------|------|----------|---------|
| `interval` | str  | no       | e.g. `5s` (default 5s). |
| `timeout`  | str  | no       | e.g. `2s` (default 2s). |
| `startPeriod` | str | no      | Grace period before probe (default 10s). |
| `command`  | list | no       | Command to run in guest; default uses a socket/ssh ping. |

### 4.2 Semantics — volumes

- **`disk`** → creates/manages `volumes/<name>.qcow2` in `.micro-compose/`.
  Attached via microvm.nix `microvm.volumes` with a matching `filesystems`
  mount at `target`. Created lazily on first `up`; never removed by `down`
  (that is `rm`).
- **`share`** → registered via `microvm.shares`. Host dir is created if
  missing. `ro` mounts use virtiofs read-only mode.
- Volume names are stack-scoped: `app-stack/db-data.qcow2`, so two stacks
  never collide.

### 4.3 Semantics — networks

- Every named network becomes a host **bridge** `br-<stack>-<net>` (or reuse a
  systemd-networkd-managed bridge declaratively when possible).
- Each VM on a network gets a **tap** `tap-<svc>-<net>` (or one shared tap per
  bridge via microvm's user networking when the bridge cannot be created).
- Guest side: the CLI generates a `systemd-networkd` stanza into each service's
  rendered module keyed by the assigned MAC, matching the pattern already used
  in `microvm-config.nix` (match by MAC, static address + gateway = bridge IP).
- Auto-IP allocation: CLI keeps a `state.json` of per-network allocated IPs so
  a restarted stack keeps the same IPs.
- **Cross-VM name resolution**: the CLI writes a hosts file into each guest
  (`/etc/hosts` entries for every service on shared networks). `dependsOn`
  implies sharing *all* networks of the dependency for name resolution, so
  `web` can `curl http://db:5432`.

### 4.4 Semantics — ports

- `hostPort:guestPort` → published on the host. Implementation options per
  hypervisor: cloud-hypervisor has no built-in port forward on tap, so the CLI
  uses either (a) `iptables DNAT` on the bridge, or (b) a tiny host-side
  `socat`/`systemd` socket unit proxying to the guest IP, or (c) `user`
  networking with `hostfwd` when the VM uses `slirp` networking instead of a
  bridge. v1 decision: **bridged networks + iptables DNAT**, with `slirp`
  networking as fallback when the host cannot create bridges (non-root or
  containerized host).

### 4.5 Semantics — ordering & health

- `up` builds a DAG from `dependsOn`; starts nodes in dependency order.
- If a node has `healthcheck`, `up` waits until it passes (or `startPeriod` +
  `interval` timeout) before starting dependents.
- `dependsOn` also short-circuits failure: if a dependency fails to boot, its
  dependents are not started and `up` exits non-zero.

---

## 5. CLI Surface

```
micro-compose [global flags] <command>

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
  -f, --file PATH   Compose file (default ./micro-compose.nix)
  -v, --verbose     Verbose output
  --dry-run         Print what would happen without doing it
```

Exit codes: `0` success, `1` operational error, `2` config/validation error,
`3` dependency-start failure.

---

## 6. Go Project Layout

```
micro-compose/
├── go.mod                     # module micro-compose; Go 1.22+
├── main.go                    # entry: cobra root command
├── internal/
│   ├── config/
│   │   ├── load.go            # eval nix→JSON, read+validate
│   │   ├── schema.go          # typed structs + validation rules
│   │   ├── eval.go            # nix-instantiate wrapper
│   │   └── validate_test.go
│   ├── nix/                   # nix tooling interop
│   │   ├── instantiate.go
│   │   ├── build.go           # nix build for runner derivations
│   │   └── flakegen/          # Go text/template that emits the stack flake
│   │       ├── flake.nix.tmpl
│   │       ├── vm-module.tmpl # per-service NixOS module (net, mounts, hosts)
│   │       └── render.go
│   ├── state/
│   │   ├── store.go           # state.json (IPs, volume names, runner paths)
│   │   └── store_test.go
│   ├── hostnet/
│   │   ├── bridge.go          # create/delete bridge, taps
│   │   ├── dnat.go            # iptables rules for port publishing
│   │   ├── ipalloc.go         # subnet IP allocator
│   │   ├── ipalloc_test.go
│   │   └── iface_unix.go      # netlink or `ip` command wrapper
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
    └── fixtures/              # sample micro-compose.nix files
```

Dependencies (keep minimal):
- `github.com/spf13/cobra` — CLI framework.
- `github.com/samber/lo` or stdlib only (prefer stdlib).
- `github.com/vishvananda/netlink` — bridge/tap setup (falls back to `ip`).
- `gopkg.in/yaml.v3` only if/when a docker-compose adapter lands.

---

## 7. State & File Layout on Disk

Everything generated lives under `<project>/.micro-compose/` (gitignored).

```
.micro-compose/
├── flake.nix               # generated: nixosConfigurations for every service
├── flake.lock
├── modules/                # generated per-service modules
│   ├── db.nix
│   └── web.nix
├── volumes/
│   └── db-data.qcow2       # persistent block volumes (name-scoped)
├── shares/                 # created on demand for relative host paths
├── runners/                # resolved microvm-run scripts
│   ├── db
│   └── web
├── logs/                   # rotation-safe run logs (or use journald)
│   └── db.log
└── state.json              # IP allocation, volume registry, statuses
```

`state.json` shape:

```json
{
  "stack": "app-stack",
  "networks": {
    "backend": {
      "cidr": "192.168.51.0/24",
      "allocated": { "db": "192.168.51.2", "web": "192.168.51.3" }
    }
  },
  "services": {
    "db": {
      "ip": { "backend": "192.168.51.2" },
      "volumes": ["db-data"],
      "status": "running",
      "pid": 4242,
      "runner": ".micro-compose/runners/db"
    }
  },
  "ports": { "5432": { "svc": "db", "guest": 5432 } }
}
```

---

## 8. Rendering Pipeline (nix side)

Given the config JSON, the CLI renders a flake. Two options for how the flake
is consumed:

**Option A — generated flake per stack (chosen for v1).**
```
flake.nix  (rendered by text/template)
├── inputs: nixpkgs, microvm
└── nixosConfigurations.<svc> =
      nixpkgs.lib.nixosSystem {
        modules = [
          microvm.nixosModules.microvm
          ./modules/<svc>.nix     # generated guest module
        ];
      };
```
- `modules/<svc>.nix` contains: `microvm.{hypervisor,vcpu,mem,volumes,shares,interfaces}`,
  `systemd.network` stanza (MAC-matched static IP + gateway), `/etc/hosts`
  entries, mount units for disks, openssh config, healthcheck systemd service.
- Runner derivation per service via `config.microvm.declaredRunner`; the CLI
  runs `nix build .#nixosConfigurations.<svc>.config.microvm.declaredRunner`.

**Option B — library API (no generated flake).** A nix `lib` that takes the
compose attrset and returns host + guest configs, so the user can integrate
directly in their own flake. Nice, but harder to iterate on first; defer to v2.

The generated host-side network config (bridges/taps) is produced **at runtime
by the CLI** (ip/netlink), *not* baked into the flake, so `up`/`down` can
create/teardown without rebuilding NixOS.

---

## 9. Lifecycle Walkthrough (`up`)

1. **Load**: eval `micro-compose.nix` → JSON → validate.
2. **Render**: write `.micro-compose/` flake + modules (idempotent).
3. **Build**: `nix build` each service's runner (cache-friendly; unchanged
   config → store hits).
4. **Provision** (only if missing / `--no-provision` to skip):
   - create bridges `br-<stack>-<net>`, assign bridge IP = subnet gateway;
   - allocate static IPs (or read from `state.json`);
   - create qcow2 disks at `volumes/<name>.qcow2` (qemu-img) if absent;
   - create tap devices and attach to bridges;
   - apply iptables DNAT for `ports`.
5. **Start**: topological order from `dependsOn`; launch each
   `microvm-run` (or the hypervisor binary directly) with the tap/socket args;
   record PID in `state.json`.
6. **Wait**: for each service, poll `healthcheck` (or wait for SSH) until ready
   or timeout; gate dependents.
7. **Report**: print table of services, IPs, published ports.

`down` reverses: stop runners (SIGTERM), remove taps/bridges/DNAT, keep
disks + state. `down --remove-volumes` also deletes qcow2 disks.

---

## 10. Security & Safety

- Bridges/taps/DNAT require root. CLI uses `sudo`-style privilege helpers and
  fails fast with clear messages when capabilities are missing; documents the
  required setcap or systemd-udevd setup.
- All state/config paths stay inside the project dir; no writes outside
  `.micro-compose/` except host network changes.
- Passwords/secrets: guest configs should use `users.users.<u>.hashedPassword`
  or keys; CLI never injects plaintext passwords into rendered files.
- The generated guest modules must **not** allow the VM to escape to host
  networking beyond its bridge: no promiscuous taps, DNAT only for declared
  ports, and (v2) optional firewall rules per service.
- `nix-instantiate --eval` runs user config — document that the compose file
  is trusted input (same trust model as the user's own nix config).

---

## 11. Error Handling & UX

- Config errors: report with Nix line info where possible; exit code 2.
- Build errors: surface the `nix build` failure excerpt (last N lines) with the
  service name; exit code 1.
- Runtime errors: include service name, PID, and last log tail; suggest
  `micro-compose logs <svc>`.
- All commands are `--dry-run`-able and idempotent where sensible.
- `ps` shows: service, status (starting/running/healthy/degraded/stopped),
  UPTIME, IPs, ports.
- Colors only when stdout is a TTY; JSON output via `--output json` for
  scripting.

---

## 12. Testing Strategy

Unit tests (Go):
- `config/validate_test.go` — schema validation edge cases (bad CIDR, missing
  volume name, unknown network ref, duplicate IPs).
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

## 13. Milestones

| Milestone | Scope | Exit criteria |
|-----------|-------|---------------|
| **M1 — Config & eval** | schema structs, `nix-instantiate` eval, validation, `config` command | `micro-compose config` prints validated JSON for the sample file. |
| **M2 — Render & build** | flake/module templates, `build` command | `micro-compose build` produces runner derivations for db+web. |
| **M3 — Host net** | bridges, taps, IP alloc, DNAT | Manual `ip link` inspection shows bridge+taps; ports reachable. |
| **M4 — Lifecycle** | `up`/`down`/`ps` | Single-VM `up`/`down` works end-to-end; volumes persist. |
| **M5 — Multi-VM** | dependsOn, health, name resolution | db+web stack up; `web` reaches `db` by hostname; web gated on db health. |
| **M6 — Observability** | `logs`, `exec`, `restart`, `rm` | All commands work against the sample stack. |
| **M7 — Hardening** | error UX, dry-run, json output, docs | Full sample stack lifecycle passes integration tests. |

---

## 14. Open Questions

1. **exec transport**: ssh-over-vsock requires a guest agent + key injection;
   simpler v1 is `exec` via `cloud-hypervisor` console or `firecracker` API.
   Decide: is a persistent sshd in every guest acceptable?
2. **Bridge management**: create bridges imperatively via netlink at `up` time,
   or declare them in host NixOS (systemd-networkd) and have the CLI just
   attach? Current leaning: imperative + `--dry-run`, with a future
   `--host-config` command that emits the NixOS module to make them static.
3. **Port publish for cloud-hypervisor**: DNAT vs socat proxy. Requires
   testing cloud-hypervisor's behavior on bridged taps.
4. **Nix eval performance**: `nix-instantiate` on large configs per command
   run; consider caching the evaluated JSON keyed by file hash.
5. **docker-compose adapter**: parse an existing `docker-compose.yml` into a
   `micro-compose.nix` (mapping images→nixos modules is lossy). Do we want a
   best-effort converter in v2?
6. **`mem`/`vcpu` defaults and overcommit**: default the same as microvm.nix
   (no overcommit) vs docker-style overcommit (all VMs declared, host may be
   smaller). Leaning: explicit is better; document required host RAM.

---

## 15. References

- microvm.nix: <https://github.com/microvm-nix/microvm.nix>
- Current repo microvm config: `microvm-config.nix` (tap `unc0`, static
  `192.168.99.2/24`, MAC-matched networkd).
- Existing flake structure to mirror for rendering: `flake.nix` in this repo.
