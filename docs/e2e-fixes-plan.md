# Live E2E Fix Plan — cloud-hypervisor can't attach taps; volume path broken

Status: **done** — live E2E passes on lappy: `microbe up` boots db/jump/web,
`psql` round-trips against db over its published port, web's Apache is
reachable via DNAT. See "Outcome" below for what actually shipped, which
diverges from this plan in two places (imageType, and two bugs found only
once the planned fixes were live).

Companion spec: `docs/microbe-plan.md` (§13.1, §9.2, §6). Docs consulted:
`microvm.nix.pdf` (tap + volumes reference).

## Background

The last live `microbe up` on the NixOS host ("lappy") started all three VMs
(db, jump, web) but every cloud-hypervisor process died immediately:

- web/jump: `CreateVirtioNet(OpenTap(TapOpen(ConfigureTap(EPERM))))`
- db:       `touch: cannot touch 'test-net/db-data.qcow2': No such file or directory`

Both failures have since been root-caused with host-side evidence. This plan
fixes them one at a time (TDD: failing test first, commit each green step),
then re-runs the live E2E.

## Root causes (confirmed)

### Bug 1 — taps pinned to root; `EnsureTaps` never reconciles ownership

Evidence:
- `/sys/class/net/mvc-40a9dc4d8a4/owner` → `0` (root), `group` → `0`.
- `/tmp/tapattach mvc-40a9dc4d8a4` as `microbe` group (uid 1000) →
  `TUNSETIFF: Operation not permitted`.

Why:
- The taps on the host were created by the **first provisioning run**, which
  used a CLI built *before* commit `25b85b3` (the `TapSpec.Owner` fix). That
  old CLI sent `Owner: 0`, so the daemon ran `TUNSETOWNER(0)` → tap pinned to
  root.
- The daemon's `EnsureTaps` (`internal/provisiond/netops.go`) checks only that
  the tap *exists* (`LinkByName`), then re-enslaves it. A stale root-owned tap
  is therefore reused as-is forever.
- cloud-hypervisor runs as uid 1000 and does `TUNSETIFF` by name to attach;
  the kernel's owner check (`owner != current_euid && !CAP_NET_ADMIN` → EPERM)
  rejects it.

The microvm.nix docs confirm the intended shape:
> `sudo ip tuntap add $IFACE_NAME mode tap user $USER` … add `multi_queue` if
> the VM has more than one CPU core.

So the tap must carry the invoking user's uid (we already send
`Owner: os.Getuid()` from `tapSpecs`), **and** `EnsureTaps` must *reconcile*
ownership, not just existence.

### Bug 2 — volume image path is relative to the runner CWD, not the volumes dir

Evidence:
- `microvm-run` (db) does `touch 'test-net/db-data.qcow2'` … `mkfs.ext4` …
  and passes `--disk path=test-net/db-data.qcow2,image_type=raw` to
  cloud-hypervisor.
- `StartService` sets `cmd.Dir = runDir` = `.microbe/runs/db`.
- `EnsureVolume` (`internal/runtime/runner.go`) creates the disk at
  `VolumeImagePath(base, stack, name)` = `.microbe/volumes/test-net/db-data.qcow2`.

Why:
- `modules/renderer.nix` emits `image = "${compose.name}/${v.name}.qcow2"` i.e.
  a **relative** path (`test-net/db-data.qcow2`), resolved against the runner
  CWD. From `runs/db` that path does not exist → `touch` fails.
- The docs say volume `image` is "Path to disk image on the host"; a relative
  path resolves against the runner's working directory. Our runner CWD
  (`runs/<svc>`) and the volume location (`volumes/<stack>/`) disagree.

Additionally there is a **format** mismatch lurking once the path is fixed:
- `EnsureVolume` creates a real **qcow2** (`qemu-img create -f qcow2`).
- microvm.nix defaults `imageType = "raw"` and `autoCreate = true`, and
  `autoCreate` **always** builds a raw file (touch + truncate + mkfs.ext4) —
  the docs: imageType "does not change format of the image created if
  autoCreate is true".
- So the coherent configuration is: `image` = absolute path, `imageType =
  "qcow2"`, `autoCreate = false` — `EnsureVolume` is the single creator.

## Work item 1 — EnsureTaps ownership reconciliation

### Change

In `internal/provisiond/netops.go` `EnsureTaps`, after `LinkByName` succeeds,
read the existing tap's owner and, if it differs from the spec (`t.Owner`),
delete and recreate it via `tapLink(t)` (which already sets the correct
`Owner`, `IFF_NO_PI|IFF_VNET_HDR`, persistent).

Concretely:

```go
existing, ok := link.(*netlink.Tuntap)
if !ok || existing.Owner != uint32(t.Owner) {
    // stale/foreign tap: delete + recreate so the VM user can attach
    if err := netlink.LinkDel(link); err != nil { ... }
    link = tapLink(t)
    if err := netlink.LinkAdd(link); err != nil { ... }
}
```

Note: `netlink.LinkByName` already populates `Owner` for a `tun` link — either
from `IFLA_TUN_OWNER` (netlink dump, `parseTuntapData`) or the sysfs fallback
(which reads `owner`, returning the uid; root shows as 0/-1). `tapLink` sets
`Owner`, and the reconcile compares the spec value.

### Tests (TDD, red → green)

1. `TestEnsureTapsReconcilesOwnership` (unit, no root): build a fake
   `*netlink.Tuntap` with `Owner: 0`; assert that the reconcile predicate
   reports "must recreate" against a spec with `Owner: 1000`, and "keep" when
   `Owner: 1000` matches. Extract the decision into a pure helper
   (e.g. `tapNeedsRecreate(spec, existing) bool`) so it is testable without a
   live tap.
2. Keep `TestTapLinkOwnership` green (already asserts Owner + VNET_HDR).
3. `go build ./... && go vet ./... && go test ./...` all green.

### Live verification

- Host still has the stale root-owned taps. `sg microbe -c 'microbe up'` with
  the new daemon must delete + recreate them (owner → 1000) and the VMs must
  get past `OpenTap`.
- Spot check: `cat /sys/class/net/mvc-*/owner` → `1000`; `/tmp/tapattach`
  (run under `sg microbe`) returns `attach OK`.

### Notes / risks

- Deleting a tap that is enslaved to a bridge is fine (netlink drops the
  member). The bridge, addresses and DNAT are left untouched.
- Idempotency preserved: an already-correct tap is kept (no churn on repeat
  runs).
- multi_queue: all fixture VMs use `vcpu = 1`, so no `IFF_MULTI_QUEUE` needed
  (docs note it only for >1 CPU). Not in scope.

## Work item 2 — volume image path + format alignment

### Change (two files)

**A. `internal/nix/flakegen/` — carry absolute image paths in `generated.nix`**

The CLI knows `o.base` (`.microbe`), so the absolute volume image path is
computable there. Add to each service in `RenderGenerated` a
`volumes` attribute: `{ name = "<name>"; image = "<abs path>"; }`, where the
path is `filepath.Abs(VolumeImagePath(base, stack, name))`.

Plumbing (choose the least-invasive cut):
- Option 1 (preferred): `Stack.Service` gains `VolumeImages map[string]string`
  (volume name → abs path). `up.go` populates it from `cfg` + `o.base` right
  after `FromConfig`; `RenderGenerated` emits it. Unit-test
  `RenderGenerated` against a stack with a volume and assert the absolute
  path appears.
- Option 2: keep `RenderGenerated` signature, but pass `base` in. More churn.

**B. `internal/nix/flakegen/modules/renderer.nix` — use the absolute path and
declare qcow2**

- `image = gen.volumes.<name>.image` (fall back to the old relative form only
  if absent, to keep unrelated tests green, or update those tests to the new
  contract — update the tests; do not keep dead fallbacks).
- `imageType = "qcow2"`, `autoCreate = false` (matching what `EnsureVolume`
  produces). Keep `size`, `mountPoint`, `fsType` as today.

### Tests (TDD, red → green)

1. `flakegen` unit test: a compose fixture with a disk volume renders
   `generated.nix` containing the service's `volumes.image` as an absolute
   path pointing at `volumes/<stack>/<name>.qcow2`.
2. `embed_test.go` marker test: `renderer.nix` must contain the new markers
   `gen.volumes`, `imageType = "qcow2"`, `autoCreate = false`.
3. Update `lifecycle_test.go` / any test asserting the old relative image
   shape.
4. Full `go test ./...` green.

### Live verification

- `microbe up` → runner's `touch 'test-net/db-data.qcow2'` now resolves to
  `.microbe/volumes/test-net/db-data.qcow2` (already created by
  `EnsureVolume`, so the `if [ ! -e ]` guard skips autoCreate).
- cloud-hypervisor `--disk path=<abs>/…qcow2,image_type=qcow2` boots db to a
  prompt; `psql` works end to end on port 5432.

### Notes / risks

- `autoCreate = false` is required; otherwise microvm.nix overwrites the
  qcow2 with a raw file (docs: autoCreate ignores imageType).
- `EnsureVolume` remains the single source of truth for volume creation and
  is already wired into `up.go` before `StartService`.
- `down --remove-volumes` already deletes `VolumeImagePath` — unchanged.

## Order & acceptance

1. Fix Bug 1 (tap ownership reconcile) → live E2E: VMs reach the volume step
   (db error disappears / advances past it), web+jump boot past `OpenTap`.
2. Fix Bug 2 (volume path + imageType) → live E2E: all three VMs stay up;
   `microbe up` table shows `running`; `curl`/`psql` reach published ports.
3. Full `go build ./... && go vet ./... && go test ./...` green after each
   commit.
4. Commit each green step (conventional commits, matching history).

## Rebuild loop (unchanged)

- Daemon (`internal/provisiond/`) changes → host rebuild in
  `/home/sky/Documents/code/nixos-config`: `nix flake update microbe &&
  sudo nixos-rebuild switch --flake .#` (restarts the socket-activated
  daemon).
- CLI-only changes → `go build -o /tmp/microbe .` (host rebuild does NOT
  update `/tmp/microbe`).
- Run live as `sg microbe -c '/tmp/microbe up …'` from `/tmp/e2e`.

## Outcome

Both planned bugs were fixed as designed, but fixing them exposed two more
bugs that only manifest once taps and volume paths are correct — this plan's
"Live verification" steps were where they surfaced, not in unit tests. All
six fixes below shipped as separate TDD commits (failing test → minimal fix →
green `go build/vet/test`), in this order:

1. `ad3cfc9` fix: reconcile tap ownership in EnsureTaps (Work Item 1, as planned)
2. `6a5010c` fix: carry absolute volume image paths into generated.nix
   (Work Item 2A, as planned)
3. `f411c38` fix: set tap group ownership, not just owner (**new bug**, found live)
4. `d947f1c` fix: format volumes as raw with mkfs, not empty qcow2
   (Work Item 2B, **diverges from plan** — see below)
5. `82bc5ba` fix: add e2fsprogs to host systemPackages for mkfs.ext4
   (**new gap**, found live, needed by #4)
6. (later) revert: restore postgres fixture, made it actually reachable with
   `enableTCPIP` + a trust rule (not a microbe bug — a test-fixture gap; a
   sqlite detour was tried and reverted per request, see git log)

### New bug: tap *group* ownership (#3)

Work Item 1's fix (owner reconciliation) was necessary but not sufficient.
The kernel checks a persistent tap's group independently of its owner
(`TUNSETGROUP` vs `TUNSETOWNER`) — `netlink.Tuntap.Group` defaulted to 0
(root) because `TapSpec` never set it, so cloud-hypervisor still got `EPERM`
on `TUNSETIFF` even after the owner fix landed and taps showed `owner=1000`
in sysfs. Fix: `TapSpec` gained a `Group` field (CLI populates it with
`os.Getgid()`), `tapLink` sets `Tuntap.Group`, and `tapNeedsRecreate`
reconciles both owner and group. Confirmed live: `owner=1000 group=970`
(the `microbe` group's gid), all three VMs got past `OpenTap`.

### Divergence: `imageType = "raw"`, not `"qcow2"` (#4)

The plan's Work Item 2 called for `imageType = "qcow2"` with `EnsureVolume`
creating a real qcow2 via `qemu-img create -f qcow2`. That part worked, but
nobody ever put a filesystem *in* the qcow2 — with `autoCreate = false`
(required to stop microvm.nix stomping the image), nothing formats it, so
the guest's mount failed: `EXT4-fs (vdb): VFS: Can't find ext4 filesystem`.

Formatting a real qcow2 container from the host requires `qemu-nbd` to
expose it as a block device first, and that needs root — which would violate
the "qemu-img, unprivileged" constraint from `docs/microbe-plan.md` §9.2
(line 538). `mkfs.<fsType>` run directly on a **raw** file's bytes works
unprivileged. So `EnsureVolume` switched to `qemu-img create -f raw` +
`mkfs.<fsType>`, and `renderer.nix` declares `imageType = "raw"` (not
`"qcow2"`) to match. The on-disk filename keeps its `.qcow2` suffix per the
path contract in `docs/microbe-plan.md` (§9.2, §8.4) even though the bytes
are raw — cloud-hypervisor uses the explicit `imageType` flag, not extension
sniffing, so this is cosmetic, not a correctness issue.

### New gap: `mkfs.ext4` not on host `$PATH` (#5)

`modules/host.nix` added `pkgs.qemu-utils` to `environment.systemPackages`
for `qemu-img`, but never added `pkgs.e2fsprogs` for `mkfs.ext4`. Once
`EnsureVolume` started calling `mkfs.ext4` (fix #4), `up` failed with
`exec: "mkfs.ext4": executable file not found in $PATH`. One-line fix, needed
a host rebuild like any other `modules/host.nix` change.

### Live verification results (lappy, `/tmp/e2e`)

- `microbe up` boots all three VMs; `microbe ps` shows all `running` and stays
  that way (checked at 10-12s post-boot, no crash-loop).
- Taps: `cat /sys/class/net/mvc-*/owner` → `1000`, `.../group` → `970`.
- db: `dd`-verified ext4 superblock magic (`53 ef`) present in the volume
  image; guest log shows `Started PostgreSQL Server`; `nix shell
  nixpkgs#postgresql -c psql -h 192.168.51.2 -p 5432 -U postgres -d postgres
  -c "select 1;"` → `1`.
- web: guest log shows `Started Apache HTTPD`; reachable directly on
  `192.168.51.3:80`.
- `sudo nft list ruleset` confirms the DNAT rules installed correctly:
  `tcp dport 5432 dnat to 192.168.51.2:5432`, `tcp dport 8080 dnat to
  192.168.51.3:80`.
- Note: `curl localhost:8080` / connecting to the host's own LAN IP from
  itself both fail — not a microbe bug. `ip route get <own-ip>` shows the
  kernel routes self-destined traffic via `lo`, which bypasses the nftables
  `prerouting` DNAT hook entirely (classic Linux hairpin-NAT limitation).
  Verified instead via direct guest-IP connections and the `nft ruleset`
  dump above; full external reachability would need a second LAN host.
