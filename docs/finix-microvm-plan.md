# finix as a microbe guest OS — implementation plan

Status: **Draft for review** · Part 1 of microbe's guest-OS flavors
Date: 2026-08-09

---

## 1. Overview & Motivation

microbe currently renders every service as a NixOS `nixosSystem` driven by
`microvm.nixosModules.microvm`. This plan adds a second guest OS: **finix**
(https://github.com/finix-community/finix), an experimental OS that replaces
systemd with `finit` as PID 1, `mdevd` for devices, and `seatd` for seats.

`services.<svc>.os = "nixos" | "finix"` (default `"nixos"`) selects the guest
flavor. Everything else in the compose surface — networks, static IPs, ports,
virtiofs volumes, vsock cid, healthchecks — is guest-OS-agnostic and unchanged.

Compatibility: `os` is a new field with a default, so existing `microbe.nix`
files that never mention it keep evaluating exactly as they do today —
omitting `os` is not a distinct state from explicitly writing `os = "nixos"`.

Scope for this milestone:
- M1 — boot a finix guest to a shell/getty under cloud-hypervisor, run by microbe.
- M2 — one wired finit service (postgres) on the stack's network, with a volume
  and a published port, the way the demo runs postgres today.

Decisions (confirmed):
- cloud-hypervisor from day one (no qemu intermediate).
- finix ships its own `services.openssh` and `services.postgresql` modules, so
  the guest-base and demo map 1:1 to today's NixOS equivalents.

---

## 2. Why it works — boot model equivalence

Both OSes boot the same way in a VM:

```
-kernel <kernel> -initrd <initrd>  <kernelParams>   # incl. init=<toplevel>/init
root = tmpfs /  +  /nix/store shared read-only from the host (9p or virtiofs)
```

- NixOS/microvm boots the systemd initramfs + `init=.../init` (microvm
  `system.nix` pins `init=.../init`); the store arrives via a store disk or a
  virtiofs share.
- finix boots its own finit initramfs (`boot.initrd.package`) + `init=.../init`.
  Its stage-1 `finit/mount.nix` mounts every `neededForBoot` filesystem into
  `/sysroot`, then `initctl switch-root /sysroot <init>` (see finix
  `finit/initrd.nix` + `virtualisation/qemu.nix` `bootMode = "kernel"`).

So a finix guest can run on cloud-hypervisor by providing, in the guest config:

| service | NixOS (today) | finix (new) |
|---|---|---|
| kernel | `boot.kernelPackages.kernel` | same |
| initramfs | systemd `initialRamdisk` | `boot.initrd.package` (`.../initrd`) |
| `init=` | NixOS toplevel | `system.topLevel/init` |
| store | store disk / virtiofs | `/nix/store` virtiofs (host, readonly) |
| root fs | — | `fileSystems."/"` tmpfs (neededForBoot) |
| nics | `systemd.network` units | finit `run:` task, post-switch-root (see §2.1) |
| ssh | `services.openssh` (systemd) | finit native `services.openssh` |
| exec agent | `vsockexec` systemd unit | same `vsockexec` binary, finit `run:` service (contingent — see §7 risk 3) |
| volumes | microvm `shares` (virtiofs) | `fileSystems` virtiofs + finit `pre:` mount task (see §2.1) |

### 2.1 finit job-type vocabulary

finit jobs used by this plan, each running in the final switched-root system
(not stage-1 initrd, which only needs `mount` + whatever `neededForBoot`
requires):

- `pre:` — runs once before the service it's attached to starts; used here to
  mount a virtiofs volume before postgres starts (§5).
- `run:` — a long-running or one-shot task under finit supervision; used here
  for the static-IP/route job (§4.3) and for `vsockexec` (§4.3, §7 risk 3).
- `ready:` — a readiness gate finit can block on; used here as the trigger a
  service's dependents (or microbe's own healthcheck) can key off (§5).

Note the scope split: stage-1 (initrd) only needs enough `ip`/mount tooling to
get the store mounted and switch-root done (§7 risk 2). The nic and volume
jobs above run *after* switch-root, in the full finix system, where the
regular (non-initrd) package set is available — so risk 2's "does busybox in
the initrd have `ip`" question does not block §4.3's nic job, which runs
post-switch-root with the full system's tools.

---

## 3. Phase 0 — finix boot material under cloud-hypervisor

New fixed part `internal/nix/flakegen/parts/finix-base.nix` (ships with
microbe, mirrors the `virtiofsd-run.nix` pattern). It defines a finix guest
module set (`flake.finixModules.finix-base`) that produces the boot chain and
filesystem layout from the table in §2, plus:

- A small **runner derivation** emitting the cloud-hypervisor argv:
  `--kernel --initrd --cmdline`, `--cpus`, `--memory`, `--net tap=...` (one per
  attached network), and a `--virtiofs` pointing at a per-stack virtiofsd for
  the store. Mirror microvm.nix `lib/buildRunner` only in spirit — this lives
  in microbe to stay hypervisor-native and free of microvm.nix's systemd
  coupling.
- `options.virtualisation.cores` / `memorySize` are reused from finix's own
  `modules/virtualisation/common.nix`.

Finix becomes a flake input of every rendered stack:

```nix
finix.url = "github:finix-community/finix";
```

Finix guests are evaluated with `finix.lib.finixSystem` (with
`specialArgs.pkgs` = the stack's nixpkgs) instead of `nixosSystem`.

### Phase 0 verification (gates Phase 1)

Phase 0 must resolve all five risks in §7 before Phase 1 starts, not just the
boot-level ones — a risk left open here becomes a silent assumption baked
into Phase 1/2 code otherwise. Concretely:

- **Boot smoke** (risks 1, 2): `microbe up` boots to the finix console lines
  (`finix - stage 1`, `entering runlevel 2` — grep on the console, the same
  assertions as finix's own `tests/boot.nix`). Confirms `boot.initrd.package`
  produces a usable `initrd` and that stage-1 mounts the virtiofs store
  successfully.
- **vsockexec smoke** (risk 3): run the existing `vsockexec` static binary as
  a bare finit `run:` job in a minimal finix guest (no other services) and
  confirm a `microbe exec` round-trip works. If this reveals real systemd
  linkage (cgroup/socket-activation dependence), that becomes a blocking
  finding written back into this plan before §4.3 is implemented as currently
  written.
- **Readiness smoke** (risk 4): a trivial finit service using `ready:` gates
  a healthcheck poll from the host, proving finit's readiness signal is
  observable the same way NixOS's healthcheck gating is today.
- **Eval-only**: a finix guest config evaluates and its runner derivation
  builds (covers the eval path independent of actually booting).

Only once all four checks above pass does Phase 1 begin.

---

## 4. Phase 1 — schema, rendering, CLI

### 4.1 Schema (`internal/config/load.go`)
- `services.<svc>.os` — `enum [ "nixos" "finix" ]`, default `"nixos"`,
  applied in `applyDefaults`.

### 4.2 Rendering (`internal/nix/flakegen`)
- Per-service part `<svc>.nix` branches on `os`:
  - `"nixos"` → today's `nixosSystem` + microvm.nix modules (unchanged).
  - `"finix"` → `finixSystem` + `finix-base` + `finix-guest-base` parts.
- Per-service **build target** recorded in `generated.json` under a new
  `buildTarget` string field per service:
  - NixOS: `.#nixosConfigurations.<svc>.config.microvm.declaredRunner`
  - finix: `.#finixConfigurations.<svc>.config.virtualisation.cloudHypervisor.runner`
  (both are derivations microbe `nix build`s and executes identically; the
  exact finix attrpath is confirmed during Phase 0's eval-only check in §3,
  since it depends on how `finix-base.nix`'s runner derivation is exposed).

### 4.3 Finix guest base
New fixed part `parts/finix-guest-base.nix` (mirror of `guest-base.nix`):
- `services.openssh.enable = true` + `PasswordAuthentication = false`,
  `root` authorized key from `generated.json.sshPublicKey`.
- `vsockexec` (reuse `parts/agent.nix` binary) as a finit `run:` service so
  `microbe exec` / `microbe shell` keep working; host-side vsock setup
  unchanged. **Contingent on Phase 0's vsockexec smoke test (§3) passing
  clean** — if that test surfaces systemd coupling, this subsection needs a
  fallback design before implementation, not just a footnote.
- Static IP + default route per attached network via one finit `run:` task,
  running post-switch-root in the full system (`ip addr add` matching MAC,
  `ip route add default` via gateway), plus `/etc/hosts` and
  `/etc/resolv.conf` from generated data. See §2.1 for why this does not
  depend on stage-1 initrd tooling.
- No firewall on managed networks (bridge is host-controlled, same as today).

### 4.4 microbe-demo
- Add the `finix` flake input to `microbe-demo/flake.nix` (mirrors how
  `microvm.nix` is already an input there) and regenerate `flake.lock`.
- This is a prerequisite for §5's demo service, not part of §5 itself, since
  every other demo service already assumes its guest-OS flake input exists
  before the service definition is added.

### 4.5 CLI (`internal/cmd/up.go`)
- Resolve the per-service build target from `generated.json`'s `buildTarget`
  field (§4.2); everything else (`up`, `ps`, `down`, volume attach, port
  DNAT, healthcheck) is untouched.

---

## 5. Phase 2 — one wired finit service: postgres

Source a demo service with `os = "finix"` in `microbe-demo` (flake input
added in §4.4):

- `services.postgresql.enable = true; enableTCPIP = true;` (finix-native
  module).
- Volume `db-data` → virtiofs host dir → `/var/lib/postgresql`, mounted by a
  finit `pre:` task (see §2.1) before postgres starts (`initdb` on first
  boot, uid-mapped like the NixOS flow).
- Port `5432:5432` DNAT and the static IP on the stack network — unchanged.
- finit readiness: a `ready:` job (see §2.1) signals postgres is accepting
  connections; host-side `healthcheck.port = 5432` unchanged, and is the same
  mechanism proven working in Phase 0's readiness smoke test (§3).

**Acceptance:** `psql -h <ip> -p 5432 -U postgres -c 'select 1;'` from the host;
both services `running`/`healthy` in the same `microbe up` table.

---

## 6. Files touched

| Area | Change |
|---|---|
| `internal/config/load.go` | `os` option + default; docs |
| `internal/nix/flakegen/parts/finix-base.nix` | new fixed part (boot + runner) |
| `internal/nix/flakegen/parts/finix-guest-base.nix` | new fixed part (ssh/nics/agent) |
| `internal/nix/flakegen/*` | per-svc part templating branches on `os`; `Stack` build-target (`buildTarget` field) |
| `internal/cmd/up.go` | per-service build-target selection |
| `microbe-demo/flake.nix` | finix flake input (§4.4), regenerated `flake.lock` |
| `microbe-demo/*` | finix service sample (§5) |
| `internal/nix/flakegen/eval_integration_test.go` | add a finix runner eval test |
| `docs/`, `tree.yml` | this plan + summary |

---

## 7. Risks / unknowns (resolved in Phase 0, gates Phase 1 — see §3)

1. finix initramfs layout: verify `boot.initrd.package` yields an `initrd`
   file (qemu module references `${boot.initrd.package}/initrd` — confirm).
   Verified by: Phase 0 boot smoke.
2. Stage-1 must mount the store share before switch-root; confirm the boot
   initrd includes `virtiofs` support + `mount` (stage-1 does not need `ip` —
   see §2.1 for why the nic job doesn't run until after switch-root).
   Verified by: Phase 0 boot smoke.
3. `vsockexec` binary currently run under systemd; confirm it runs as a bare
   finit `run:` job with no systemd linkage (cgroup/socket activation).
   Verified by: Phase 0 vsockexec smoke. §4.3 is written assuming this
   passes; if it doesn't, §4.3 needs a redesign before implementation.
4. Behavioral drift: finit readiness (`ready:`/`pre:`) vs systemd ordering;
   `dependsOn`/healthcheck gating remains host-side so is unaffected by the
   guest-side mechanism, but the guest-side `ready:` signal itself needs
   proving. Verified by: Phase 0 readiness smoke.
5. Cloud-hypervisor quirks: `--virtiofs` requires a separate
   `virtiofsd` process per share (the demo already does this for volumes —
   reuse that machinery for the store). Verified by: Phase 0 boot smoke
   (store share) and Phase 2 acceptance (volume share).
