# finix as a microbe guest OS — implementation plan

Status: **Phase 0 in progress** · Part 1 of microbe's guest-OS flavors
Date: 2026-08-09

## Phase 0 status (2026-08-09)

`os = "nixos" | "finix"` is wired end-to-end through config schema,
validation, `flakegen.Stack`/`generated.json`, and `ServicePart`'s
finixSystem branch (see `internal/nix/flakegen/parts/finix-base.nix`).
`TestFinixStackEvaluates` proves a finix service evaluates against the real
`finix-community/finix` flake input and produces a buildable
`microbe.qemuRunner` derivation.

**Real assumptions this superseded** (the original §2-§7 below described an
earlier, unverified design; kept for the boot-model reasoning, but these
specifics turned out wrong against the actual finix flake):
- finix has no cloud-hypervisor backend at all — only QEMU
  (`modules/virtualisation/qemu.nix`). Phase 0 boots under QEMU.
- `finixSystem` takes no `system` arg and no implicit nixpkgs; `lib` and
  `specialArgs.pkgs` must be passed explicitly.
- finit has no `pre:`/`run:`/`ready:` vocabulary — the real API is
  `finit.services.<name>` / `finit.tasks.<name>` submodules with
  `conditions`/`notify`/`command`.
- `virtualisation.qemu.argv` is a plain list of strings, not a buildable
  derivation — `finix-base.nix` wraps it in its own `microbe.qemuRunner`
  (`pkgs.writeShellScriptBin`) since finix exposes no equivalent to
  microvm.nix's `declaredRunner`.
- `virtualisation/qemu.nix` isn't auto-imported by finix's own
  `nixosModules.default` — `ServicePart`'s finix branch imports it by path
  from the finix flake input instead.

**Verified by real boot** (`nix build` the runner, run it under
`-nographic`, read the console): kernel/initrd/`init=` handoff, virtio
module loading, and finit stage 1 all confirmed correct — console output
matches finix's own `tests/boot.nix` markers exactly
(`finix - stage 1, entering runlevel S`).

**Bug found and fixed**: qemu.nix's `-virtfs` shorthand for the
`/nix/store` share didn't attach a working virtio-9p channel under this
qemu/machine-type combination (`9pnet_virtio: no channels available`,
confirmed live). `finix-base.nix` now rewrites `virtualisation.qemu.argv`
to drop `-virtfs` and use the equivalent explicit `-fsdev` + `-device
virtio-9p-pci` pair instead — verified live: `mount-nix-store` now
succeeds.

**Second bug found and fixed**: `switch-root` was silently rebooting
~10s after starting, no error printed. Root cause: finix declares no
default root filesystem (unlike NixOS) — without `fileSystems."/"`,
nothing has mountpoint `/`, `neededForBoot` forcing never fires,
`/sysroot` never gets mounted, and stage-1's switch-root task takes its
"not a mountpoint" failure branch, which just prints "rescue shell is
disabled / rebooting in 10s" and reboots with no further diagnostic.
`finix-base.nix` now declares `fileSystems."/" = { device = "tmpfs";
fsType = "tmpfs"; options = ["mode=755"]; }`, mirroring what finix's own
test harness (`tests/lib/default.nix`) sets for every VM it boots.

**Verified live after the fix**: `switch_root` succeeds, stage 2 is
reached (`<<< finix - stage 2 >>>`), and finit enters runlevel 2
(`finix, entering runlevel 2`) — confirmed on 3 separate boots. The
`/nix/store` 9p attach (the first bug's fix) is intermittently flaky
under this host — 1 of 3 boots hit a transient `9pnet_virtio: no
channels available` on the store mount specifically, unrelated to the
switch-root fix; retrying reliably works. Not yet investigated further
since it self-heals on retry and doesn't block boot when it doesn't
occur.

**Third bug found, fix in place, not fully resolved**: the `/nix/store`
9p mount (fixed above) was intermittently failing on ~half of boots with
the same `9pnet_virtio: no channels available` symptom — a well-known,
pre-existing kernel/qemu race between virtio_pci binding the device and
finit's mount attempt running (not introduced by the `-fsdev`/`-device`
rewrite itself, confirmed via web search against existing kernel-list
and forum reports of the same race on other distros). Mitigated by
overriding `boot.initrd.finit.tasks."mount-nix-.ro-store".script` with a
short retry loop (up to 50 tries, 200ms apart) instead of finit's
generated single-shot mount command.

**"Hang" investigated at length (§history below) and resolved: it was
never a hang.** Boot reliably reaches `finix, entering runlevel 2`, runs
`loadkmap`/`suid-sgid-wrappers`/`sysctl`/`tmpfiles-setup`, and finit sits
idle afterward — which looked like a stall because nothing more printed
to the console being watched. Root cause: `services.getty` only spawns
gettys on `tty1`-`tty6` (virtual consoles) by default. Those are
invisible under `-nographic`/QEMU serial redirection, and disjoint from
`boot.kernelParams`'s `console=ttyS0` (that only routes kernel/finit *log*
output — it doesn't add a getty). There was never a login prompt to see
because no getty ever targeted the serial line. Fix: `finix-base.nix` now
sets `services.getty.ttys` to include `"ttyS0"`. Verified live: `finix
login:` appears ~5s after boot, on stock finit 4.17 — no finit version
pin, no `libconfuse` dependency, no other workaround needed. The extended
investigation below (finit version pin, QMP register sampling, `/proc/1`
process-state dumps) is kept for the debugging technique/methodology, but
its "PID 1 stuck in userspace" conclusion was wrong — `epoll_wait` with
nothing left to do *is* what a correctly-idling finit looks like once
runlevel 2 finishes; the diagnostic dumps just never had a getty on the
console to make that visible.

<details>
<summary>Investigation history (superseded — kept for technique reference)</summary>

Confirmed hung, not slow: booted with a 150s wall-clock window (vs.
~5s of guest boot time needed to reach this point) and the guest kernel
clock in dmesg timestamps never advances past `5.246848` for the
remaining ~145 real seconds — the qemu process keeps running (not
crashed, not exited), but the guest makes zero further progress. Ruled
out console-output buffering as the explanation (retried with explicit
`-serial file:...` instead of `-nographic`'s multiplexed stdio; same
stall point). *(In hindsight: correct observation, wrong inference —
nothing more ever prints because there was no getty on that console, not
because the guest stopped making progress.)*

Overriding `finit.tasks.sysctl.command` with a trivial `echo DIAG-...;
exit 0` stub still "hung" at exactly the same point. Ran the
`services.getty.enable = false` diagnostic — identical "hang" with getty
on vs. off (`console9.log` vs. `console_nogetty.log`) — correctly ruled
out *whether getty runs at all* as the cause, but the diagnostic never
considered *which tty* getty targets, so it missed the real issue right
next to it.

QMP `query-status` while "hung" returned `{"status": "running", "running":
true}`, read at the time as "vCPU spinning/livelocked". Revisited later
with `info registers` sampled 3x: `RIP` constant, `HLT=1` — actually the
kernel idle loop, CPU genuinely parked waiting for interrupts, a counter
register incrementing sample to sample confirming timer interrupts still
firing. The kernel was fine the whole time.

Found `https://github.com/aanderse/finix-config` (real hardware, not a
VM), which pins `finit.package` to a post-4.17 commit
(`finit-project/finit@d2781ef6`, self-labeled `version = "5.0"`). Mirrored
that pin (`nix-prefetch-git` for the hash; needed `pkgs.libconfuse` added
to `buildInputs` since that commit range moved `finit.conf` to a new
"block format" parser requiring `libconfuse >= 3.3`). Built and booted:
getty registration order changed slightly but the same "hang" point
persisted. Correctly concluded "finit version isn't the cause" — but this
row was a red herring the whole time; the version pin is not in the final
fix.

`/proc/1` dumped every second via an independent finit task
(`finit.tasks."aaa-diag"`, forked alongside `sysctl` so it survives even
if PID 1 "wedges") showed the true state: `sysctl`'s PID briefly zombied
then got reaped normally, `State: S (sleeping)`, `wchan: do_epoll_wait`,
stack `do_epoll_wait → __x64_sys_epoll_wait → do_syscall_64` — completely
ordinary idle-event-loop behavior for an init process with nothing
queued. This dump is what triggered the "wait, is this maybe just... not
hung?" reconsideration, followed by testing `services.getty.ttys = [
... "ttyS0" ]` directly, which produced the login prompt immediately and
closed the investigation.

</details>

## AF_VSOCK for finix guests (2026-08-09)

`microbe shell`/`microbe exec` reach NixOS guests over vsock: cloud-hypervisor
exposes a hybrid-vsock UNIX socket (`notify.vsock`, dialed via
`vsockexec.DialHybridVsock`'s `CONNECT <port>\n` handshake) that
`microbe-agent` (a stdlib-only Go binary, `internal/nix/flakegen/agent/main.go`)
listens on, run as a systemd unit (`parts/agent.nix`). None of this existed
for finix guests: no vsock device on the QEMU command line, no agent
running, and the host-side dial code only knew the hybrid-UDS scheme, which
doesn't apply to finix's plain-QEMU boot path (no cloud-hypervisor
involved) or its `finit` init system (no systemd).

**Wired up, real kernel AF_VSOCK instead of hybrid vsock** (finix uses
plain QEMU, whose `vhost-vsock-pci` device is the traditional
CID-addressed kernel transport — a different mechanism from
cloud-hypervisor's hybrid UDS scheme, not just a different unit type):

- `parts/finix-agent.nix` (new): adds
  `-device vhost-vsock-pci,id=vsock0,guest-cid=<cid>` to the guest's QEMU
  args (CID read back from `generated.json`, the same value NixOS guests
  get via `microvm.vsock.cid` — CID assignment itself was already
  OS-agnostic, `stack.go` assigns one to every service regardless of `os`)
  and runs `microbe-agent` as a `finit.services` unit (`respawn`/
  `restart_sec` standing in for systemd's `Restart=always`/`RestartSec`).
  `virtualisation.qemu.extraArgs` is a pure addition here — finix's own
  `qemu.nix` appends it to `argv` itself, so `finix-base.nix`'s existing
  `-virtfs`-stripping argv rewrite passes it through untouched, no changes
  needed there.
- `flakegen.LoadGeneratedCID` (new): `generated.json` had no Go-side
  reader before this — `RenderGenerated` only ever wrote it. Needed so
  `resolveGuestVsock` can find a finix guest's CID at `shell`/`exec` time.
- `vsockexec.DialVsock` (new): real `AF_VSOCK` connect via
  `golang.org/x/sys/unix` (already a direct dependency), with a retry loop
  (10s/200ms, matching `finix-base.nix`'s own boot-time mount-retry
  cadence) since there's no socket file to poll for existence the way
  `DialHybridVsock`'s UDS can be — the guest's agent just takes a few
  seconds after boot before finit has it listening. Returns `*os.File`,
  not `net.Conn`: Go's `net.FileConn` rejects `AF_VSOCK` sockets outright
  (the `net` package doesn't recognize the address family) — same reason
  the guest-side agent already hands its accepted connections around as
  `*os.File` rather than wrapping them.
- `agentsession.go`: `resolveGuestVsock`/`dialAgent` now dispatch on the
  service's `os` through a single `guestAddr{OS, UDSPath, CID}`-typed seam
  (nixos → `UDSPath`, finix → `CID`) instead of a bare path string —
  one seam for tests to fake, not two.

**Verified**: `go test ./...` (192 passed, including a real loopback
`AF_VSOCK` round-trip test — this dev host has the `vsock`/
`vsock_loopback` kernel modules loaded, so `DialVsock` exercises a real
kernel socket end-to-end even without a guest). `TestFinixStackEvaluates`
(a real `nix eval`) passes with the new module wired in. Built the actual
runner by hand: the rendered QEMU command line carries
`guest-cid=3` matching `generated.json`, and the rendered finit config is
`service [234] name:microbe-agent cgroup.system restart:10 restart_sec:1
respawn notify:none .../bin/microbe-agent`.

**Not verified**: an actual booted guest answering a real `DialVsock` call
end-to-end. That needs the host's `vhost_vsock` kernel module loaded
(`/dev/vhost-vsock` doesn't exist on this dev host currently) and root to
load it — noted as the operational prerequisite in `finix-agent.nix`
itself. `modprobe vhost_vsock`, then `microbe up` a finix service and
`microbe shell` it, is the remaining real-world check.

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
