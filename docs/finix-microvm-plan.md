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
| nics | `systemd.network` units | finit `run` task with `ip` (MAC-matched) |
| ssh | `services.openssh` (systemd) | finit native `services.openssh` |
| exec agent | `vsockexec` systemd unit | same `vsockexec` binary, finit service |
| volumes | microvm `shares` (virtiofs) | `fileSystems` virtiofs + finit mount task |

---

## 3. Phase 0 — finix boot material under cloud-hypervisor

New fixed part `internal/nix/flakegen/parts/finix-base.nix` (ships with
microbe, mirrors the `virtiofsd-run.nix` pattern). It defines a finix guest
module set (`flake.finixModules.finix-base`) that produces:

- `boot.kernelPackages.kernel` + finix initrd (`boot.initrd.package`), and
  `kernelParams = [ "init=${config.system.topLevel}/init" "console=ttyS0" ... ]`.
- `fileSystems` (all `neededForBoot = true`):
  - `/` → tmpfs
  - `/nix/store` → virtiofs, tag `nix-store`, source = host `/nix/store`,
    read-only; requires `boot.initrd.supportedFilesystems."virtiofs".enable`
    + `"virtiofs"` in `boot.initrd.availableKernelModules`.
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

### Phase 0 verification
Eval-only: a finix guest config evaluates and its runner derivation builds.
Boot smoke: `microbe up` boots to the finix console lines (`finix - stage 1`,
`entering runlevel 2` — grep on the console, the same assertions as finix's
own `tests/boot.nix`).

---

## 4. Phase 1 — schema, rendering, CLI

### 4.1 Schema (`internal/config/load.go`)
- `services.<svc>.os` — `enum [ "nixos" "finix" ]`, default `"nixos"`,
  applied in `applyDefaults`.

### 4.2 Rendering (`internal/nix/flakegen`)
- Per-service part `<svc>.nix` branches on `os`:
  - `"nixos"` → today's `nixosSystem` + microvm.nix modules (unchanged).
  - `"finix"` → `finixSystem` + `finix-base` + `finix-guest-base` parts.
- Per-service **build target** recorded in `generated.json`:
  - NixOS: `.#nixosConfigurations.<svc>.config.microvm.declaredRunner`
  - finix: `.#finixConfigurations.<svc>.config.virtualisation.k <runner>`
  (both are derivations microbe `nix build`s and executes identically).

### 4.3 Finix guest base
New fixed part `parts/finix-guest-base.nix` (mirror of `guest-base.nix`):
- `services.openssh.enable = true` + `PasswordAuthentication = false`,
  `root` authorized key from `generated.json.sshPublicKey`.
- `vsockexec` (reuse `parts/agent.nix` binary) as a `finit` service so
  `microbe exec` / `microbe shell` keep working; host-side vsock setup
  unchanged.
- Static IP + default route per attached network via one `finit` `run` task
  (`ip addr add` matching MAC, `ip route add default` via gateway), plus
  `/etc/hosts` and `/etc/resolv.conf` from generated data.
- No firewall on managed networks (bridge is host-controlled, same as today).

### 4.4 CLI (`internal/cmd/up.go`)
- Resolve the per-service build target from `generated.json`; everything else
  (`up`, `ps`, `down`, volume attach, port DNAT, healthcheck) is untouched.

---

## 5. Phase 2 — one wired finit service: postgres

Source a demo service with `os = "finix"`:

- `services.postgresql.enable = true; enableTCPIP = true;` (finix-native
  module).
- Volume `db-data` → virtiofs host dir → `/var/lib/postgresql`, mounted by a
  finit `run`/`pre:` task before postgres starts (`initdb` on first boot,
  uid-mapped like the NixOS flow).
- Port `5432:5432` DNAT and the static IP on the stack network — unchanged.
- finit readiness: a `ready:` job / `initctl`-driven startup, host-side
  `healthcheck.port = 5432` unchanged.

**Acceptance:** `psql -h <ip> -p 5432 -U postgres -c 'select 1;'` from the host;
both services `running`/`healthy` in the same `microbe up` table.

---

## 6. Files touched

| Area | Change |
|---|---|
| `internal/config/load.go` | `os` option + default; docs |
| `internal/nix/flakegen/parts/finix-base.nix` | new fixed part (boot + runner) |
| `internal/nix/flakegen/parts/finix-guest-base.nix` | new fixed part (ssh/nics/agent) |
| `internal/nix/flakegen/*` | per-svc part templating branches on `os`; `Stack` build-target |
| `internal/cmd/up.go` | per-service build-target selection |
| `microbe-demo/*` | finix flake input, finix service sample, `flake.lock` |
| `internal/nix/flakegen/eval_integration_test.go` | add a finix runner eval test |
| `docs/`, `tree.yml` | this plan + summary |

---

## 7. Risks / unknowns (resolve in Phase 0)

1. finix initramfs layout: verify `boot.initrd.package` yields an `initrd`
   file (qemu module references `${boot.initrd.package}/initrd` — confirm).
2. Stage-1 must mount the store share before switch-root; confirm the boot
   initrd includes `virtiofs` support + `mount`/`ip` (busybox covers ip?).
3. `vsockexec` binary currently run under systemd; run the same static binary
   as a `finit` service — confirm no systemd linkage (cgroup/socket activation).
4. Behavial drift: finit readiness (`ready:`/`pre:`) vs systemd ordering;
   `dependsOn`/healthcheck gating remains host-side so is unaffected.
5. Cloud-hypervisor quirks: `--virtiofs` requires a separate
   `virtiofsd` process per share (the demo already does this for volumes —
   reuse that machinery for the store).