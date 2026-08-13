package flakegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"microbe/internal/config"
)

// withVolumeImages populates the generated image path for the db-data disk
// declared in testdata/microbe.nix, matching what up.go would compute from
// the CLI's data dir.
func withVolumeImages(dataDir string, st *Stack) {
	db := st.Services["db"]
	db.VolumeImages = map[string]string{
		"db-data": filepath.Join(dataDir, "volumes", "db-data.qcow2"),
	}
	st.Services["db"] = db
}

// TestGeneratedStackEvaluates is the M2 exit-criteria check: the rendered
// stack must evaluate against real microvm.nix so that every service has a
// declaredRunner derivation. It only evaluates (cheap); it does not realize
// the closure. Skipped when nix is unavailable.
func TestGeneratedStackEvaluates(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	user, err := os.ReadFile("testdata/microbe.nix")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), user, 0o644); err != nil {
		t.Fatal(err)
	}
	st := mustStack(t, fixtureConfig())
	withVolumeImages(dir, st)
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	for _, svc := range []string{"db", "jump", "web"} {
		target := ".#nixosConfigurations." + svc + ".config.microvm.declaredRunner"
		cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", target)
		cmd.Dir = dir
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("service %s did not evaluate: %v\n%s", svc, err, stderr.String())
		}
		if !strings.Contains(string(out), "microvm") {
			t.Errorf("service %s: declaredRunner = %s, want a microvm store path", svc, out)
		}
	}
}

// TestFinixStackEvaluates is Phase 0's eval-only gate for finix guests: a
// service declaring os = "finix" must produce a buildable
// microbe.qemuRunner derivation via finix's real flake input (finixSystem +
// a direct-path import of finix's own virtualisation/qemu.nix, see
// flake.go's finix branch of ServicePart and parts/finix-base.nix).
func TestFinixStackEvaluates(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	userNix := `{
  name = "finix-test";
  networks = { backend = { }; };
  services = {
    a = {
      os = "finix";
      networks = [ { name = "backend"; } ];
    };
  };
}
`
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), []byte(userNix), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Compose{
		SchemaVersion: 1,
		Name:          "finix-test",
		Networks:      map[string]config.Network{"backend": {}},
		Services: map[string]config.Service{
			"a": {OS: "finix", Networks: []config.Attach{{Name: "backend"}}},
		},
	}
	st := mustStack(t, cfg)
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	// A real `nix build`, not just `nix eval`: qemuRunner is now a
	// symlinkJoin of bin/microvm-run (cloud-hypervisor invocation) and
	// bin/virtiofsd-run (mandatory /nix/store share, see finix-base.nix/
	// finix-virtiofsd-run.nix) -- proves both actually realize, not just
	// that the attrpath evaluates.
	target := st.Services["a"].BuildTarget

	// Dry-run preflight: skip if any derivations need to be built or fetched.
	// This avoids unrecoverable hangs on cold machines where the finix closure
	// is not yet in the store.
	{
		dryCmd := exec.Command("nix", "build", "--dry-run", "--no-write-lock-file", "--no-link", target)
		dryCmd.Dir = dir
		var dryStderr strings.Builder
		dryCmd.Stderr = &dryStderr
		_ = dryCmd.Run()
		dryOut := dryStderr.String()
		if strings.Contains(dryOut, "will be built:") || strings.Contains(dryOut, "will be fetched") || strings.Contains(dryOut, "paths will be fetched") {
			t.Skip("finix closure not in store (dry-run detected builds/fetches); skipping to avoid timeout")
		}
	}

	cmd := exec.Command("nix", "build", "--no-write-lock-file", "--print-out-paths", "--no-link", target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", target, err, stderr.String())
	}
	outPath := strings.TrimSpace(string(out))
	// bin/microvm-run, not bin/run-vm: runtime.StartService (internal/
	// runtime/runner.go) hardcodes that binName for every service
	// regardless of OS, so finix's qemuRunner derivation must ship its
	// script under that name for `microbe up` to find it.
	for _, bin := range []string{"microvm-run", "virtiofsd-run"} {
		if _, err := os.Stat(filepath.Join(outPath, "bin", bin)); err != nil {
			t.Errorf("qemuRunner build %s missing bin/%s: %v", outPath, bin, err)
		}
	}
}

// TestMultipleFinixServicesEvaluate proves two finix services can coexist in
// one stack. flake.nixosConfigurations is declared by flake-parts itself as
// a lazyAttrsOf, so per-service files each contributing one key merge fine;
// flake.finixConfigurations has no such declaration anywhere (not in
// flake-parts, not in microbe's own fixed modules) until one is added, so a
// second finix service's file collides with the first ("defined multiple
// times") instead of merging.
func TestMultipleFinixServicesEvaluate(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	userNix := `{
  name = "finix-multi-test";
  networks = { backend = { }; };
  services = {
    a = {
      os = "finix";
      networks = [ { name = "backend"; } ];
    };
    b = {
      os = "finix";
      networks = [ { name = "backend"; } ];
    };
  };
}
`
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), []byte(userNix), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Compose{
		SchemaVersion: 1,
		Name:          "finix-multi-test",
		Networks:      map[string]config.Network{"backend": {}},
		Services: map[string]config.Service{
			"a": {OS: "finix", Networks: []config.Attach{{Name: "backend"}}},
			"b": {OS: "finix", Networks: []config.Attach{{Name: "backend"}}},
		},
	}
	st := mustStack(t, cfg)
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	target := st.Services["b"].BuildTarget
	cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if _, err := cmd.Output(); err != nil {
		t.Fatalf("eval %s: %v\n%s", target, err, stderr.String())
	}
}

// TestFinixStoreMountCreatesMountpoint is a regression test for a real
// boot hang found via a live cloud-hypervisor boot: finix-base.nix's
// retry-loop override of the "mount-nix-.ro-store" task (see the task's
// own comment) hand-writes the `mount -t virtiofs` command instead of
// using finix's own auto-generated one (mount.nix's `mount -o ${opts}`,
// where opts always includes "X-mount.mkdir" to create the mountpoint
// directory first). Without -o X-mount.mkdir, /sysroot/nix/.ro-store never
// gets created, so the mount fails on literally every one of the 50
// retries (mount point does not exist -- not the PCI-probe race the retry
// loop was written for), the task never succeeds, and everything
// downstream (the /nix/store bind mount, switch-root's init= existence
// check) fails in turn, reboot-looping the guest. Console output alone
// hid this: finit's boot-progress UI prints "[ OK ]" for a task as soon as
// it *starts*, not when it completes, so a task silently failing in the
// background for the next ~10s looks identical to one that already
// succeeded.
func TestFinixStoreMountCreatesMountpoint(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	userNix := `{
  name = "finix-bindmount-test";
  networks = { backend = { }; };
  services = {
    a = {
      os = "finix";
      networks = [ { name = "backend"; } ];
    };
  };
}
`
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), []byte(userNix), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Compose{
		SchemaVersion: 1,
		Name:          "finix-bindmount-test",
		Networks:      map[string]config.Network{"backend": {}},
		Services: map[string]config.Service{
			"a": {OS: "finix", Networks: []config.Attach{{Name: "backend"}}},
		},
	}
	st := mustStack(t, cfg)
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	target := ".#finixConfigurations.a.config.boot.initrd.finit.tasks.\"mount-nix-.ro-store\""
	cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("eval %s: %v\n%s", target, err, stderr.String())
	}

	var task struct {
		Script string `json:"script"`
	}
	if err := json.Unmarshal(out, &task); err != nil {
		t.Fatalf("unmarshal task: %v\n%s", err, out)
	}
	if !strings.Contains(task.Script, "X-mount.mkdir") {
		t.Errorf("mount-nix-.ro-store script never passes -o X-mount.mkdir -- the mountpoint directory never gets created, so every mount attempt fails regardless of retries:\n%s", task.Script)
	}
}

// TestFinixRunnerSetsInitKernelParam is a regression test for a real boot
// hang found via a live cloud-hypervisor boot: finix-base.nix builds its
// own cloud-hypervisor --cmdline (finix's own virtualisation/qemu.nix,
// which used to supply "init=${config.system.topLevel}/init", is no longer
// imported at all -- see the module's own header comment). Without an
// init= kernel param, stage-1's switch-root script (finix's
// modules/finit/initrd.nix) falls back to its "/init" default, which
// nothing ever creates in the tmpfs root, so switch-root always takes its
// failure path ("rescue shell is disabled, rebooting in 10s") -- verified
// live: the guest boots, mounts everything correctly, then reboot-loops
// forever right at switch-root without this.
func TestFinixRunnerSetsInitKernelParam(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	userNix := `{
  name = "finix-init-param-test";
  networks = { backend = { }; };
  services = {
    a = {
      os = "finix";
      networks = [ { name = "backend"; } ];
    };
  };
}
`
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), []byte(userNix), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Compose{
		SchemaVersion: 1,
		Name:          "finix-init-param-test",
		Networks:      map[string]config.Network{"backend": {}},
		Services: map[string]config.Service{
			"a": {OS: "finix", Networks: []config.Attach{{Name: "backend"}}},
		},
	}
	st := mustStack(t, cfg)
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	target := st.Services["a"].BuildTarget

	// Dry-run preflight: skip if any derivations need to be built or fetched.
	{
		dryCmd := exec.Command("nix", "build", "--dry-run", "--no-write-lock-file", "--no-link", target)
		dryCmd.Dir = dir
		var dryStderr strings.Builder
		dryCmd.Stderr = &dryStderr
		_ = dryCmd.Run()
		dryOut := dryStderr.String()
		if strings.Contains(dryOut, "will be built:") || strings.Contains(dryOut, "will be fetched") || strings.Contains(dryOut, "paths will be fetched") {
			t.Skip("finix closure not in store (dry-run detected builds/fetches); skipping to avoid timeout")
		}
	}

	cmd := exec.Command("nix", "build", "--no-write-lock-file", "--print-out-paths", "--no-link", target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", target, err, stderr.String())
	}
	outPath := strings.TrimSpace(string(out))

	script, err := os.ReadFile(filepath.Join(outPath, "bin", "microvm-run"))
	if err != nil {
		t.Fatalf("read bin/microvm-run: %v", err)
	}
	if !strings.Contains(string(script), "init=/nix/store") {
		t.Errorf("bin/microvm-run --cmdline has no init= kernel param (switch-root always fails without it):\n%s", script)
	}
}

// TestFinixShareVolumeGetsMountTask is a regression test for a real gap
// found via live boot testing: finix has no stage-2 equivalent of NixOS's
// systemd automount for ordinary `fileSystems` entries (grepped finix's
// own modules/finit/stage2.nix: no fileSystems handling at all -- only
// stage-1's mount.nix handles neededForBoot entries, and user share
// volumes are deliberately not neededForBoot). Without an explicit
// post-switch-root finit task, a declared share volume stays declared in
// fileSystems but never actually gets mounted, verified live (`mountpoint
// -q` on the share's target failed in a booted guest despite the
// virtiofs device being attached).
func TestFinixShareVolumeGetsMountTask(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	shareHost := t.TempDir()
	userNix := `{
  name = "finix-share-mount-test";
  networks = { backend = { }; };
  services = {
    a = {
      os = "finix";
      networks = [ { name = "backend"; } ];
      volumes = [
        { name = "shared"; host = "` + shareHost + `"; target = "/mnt/shared"; }
      ];
    };
  };
}
`
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), []byte(userNix), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Compose{
		SchemaVersion: 1,
		Name:          "finix-share-mount-test",
		Networks:      map[string]config.Network{"backend": {}},
		Services: map[string]config.Service{
			"a": {
				OS:       "finix",
				Networks: []config.Attach{{Name: "backend"}},
				Volumes:  []config.Volume{{Name: "shared", Type: "share", Protocol: "virtiofs", Host: shareHost, Target: "/mnt/shared"}},
			},
		},
	}
	st := mustStack(t, cfg)
	svc := st.Services["a"]
	svc.ShareHosts = map[string]string{"shared": shareHost}
	st.Services["a"] = svc
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	target := ".#finixConfigurations.a.config.finit.tasks.\"mount-share-shared\".command"

	// Dry-run preflight: skip if any derivations need to be built or fetched.
	{
		dryCmd := exec.Command("nix", "build", "--dry-run", "--no-write-lock-file", "--no-link", target)
		dryCmd.Dir = dir
		var dryStderr strings.Builder
		dryCmd.Stderr = &dryStderr
		_ = dryCmd.Run()
		dryOut := dryStderr.String()
		if strings.Contains(dryOut, "will be built:") || strings.Contains(dryOut, "will be fetched") || strings.Contains(dryOut, "paths will be fetched") {
			t.Skip("finix closure not in store (dry-run detected builds/fetches); skipping to avoid timeout")
		}
	}

	cmd := exec.Command("nix", "build", "--no-write-lock-file", "--print-out-paths", "--no-link", target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", target, err, stderr.String())
	}
	scriptPath := strings.TrimSpace(string(out))
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", scriptPath, err)
	}
	if !strings.Contains(string(script), "mount") || !strings.Contains(string(script), "/mnt/shared") {
		t.Errorf("mount-share-shared command doesn't mount /mnt/shared:\n%s", script)
	}
}

// TestShareOwnerTranslatesUidGid proves renderer.nix's owner-translation
// path end to end: a share volume declaring owner = "postgres" (a user
// the guest config actually creates via services.postgresql.enable) must
// resolve that user's guest uid/gid from the guest's own evaluated user
// database and combine it with the Go-computed host uid/gid (generated.json)
// into virtiofsd --translate-uid/--translate-gid args, with posixAcl
// disabled (the two are mutually exclusive).
func TestShareOwnerTranslatesUidGid(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	shareHost := t.TempDir()
	userNix := `{
  name = "share-owner-test";
  networks = { backend = { }; };
  services = {
    db = {
      config = { ... }: { services.postgresql.enable = true; };
      volumes = [
        { type = "share"; name = "data"; host = "` + shareHost + `"; target = "/var/lib/postgresql"; owner = "postgres"; }
      ];
      networks = [ { name = "backend"; } ];
    };
  };
}
`
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), []byte(userNix), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Compose{
		SchemaVersion: 1,
		Name:          "share-owner-test",
		Networks:      map[string]config.Network{"backend": {}},
		Services: map[string]config.Service{
			"db": {Networks: []config.Attach{{Name: "backend"}}},
		},
	}
	st := mustStack(t, cfg)
	db := st.Services["db"]
	db.ShareOwners = map[string]ShareOwner{"data": {HostUID: 1000, HostGID: 100}}
	db.ShareHosts = map[string]string{"data": shareHost}
	st.Services["db"] = db
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	target := ".#nixosConfigurations.db.config.microvm.shares"
	cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("eval %s: %v\n%s", target, err, stderr.String())
	}

	var shares []struct {
		PosixAcl  bool     `json:"posixAcl"`
		ExtraArgs []string `json:"extraArgs"`
	}
	if err := json.Unmarshal(out, &shares); err != nil {
		t.Fatalf("unmarshal shares: %v\n%s", err, out)
	}
	if len(shares) != 1 {
		t.Fatalf("shares = %d, want 1", len(shares))
	}
	share := shares[0]
	if share.PosixAcl {
		t.Error("posixAcl = true, want false (incompatible with translate-uid/gid)")
	}
	joined := strings.Join(share.ExtraArgs, " ")
	if !strings.Contains(joined, "--translate-uid map:71:${MICROBE_HOST_UID_DATA}:1") {
		t.Errorf("extraArgs = %v, want --translate-uid map:71:${MICROBE_HOST_UID_DATA}:1 (postgres guest uid 71 -> shell var for host uid)", share.ExtraArgs)
	}
	if !strings.Contains(joined, "--translate-gid map:71:${MICROBE_HOST_GID_DATA}:1") {
		t.Errorf("extraArgs = %v, want --translate-gid map:71:${MICROBE_HOST_GID_DATA}:1 (postgres guest gid 71 -> shell var for host gid)", share.ExtraArgs)
	}
}

// TestDefaultHypervisorIsCloudHypervisor pins the renderer's default when a
// compose service omits `hypervisor`: it must fall back to cloud-hypervisor
// (matching config.DefaultHypervisor), not qemu.
func TestDefaultHypervisorIsCloudHypervisor(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	user, err := os.ReadFile("testdata/microbe.nix")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "microbe.nix"), user, 0o644); err != nil {
		t.Fatal(err)
	}
	st := mustStack(t, fixtureConfig())
	withVolumeImages(dir, st)
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	target := ".#nixosConfigurations.db.config.microvm.hypervisor"
	cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("eval %s: %v\n%s", target, err, stderr.String())
	}
	if got := strings.TrimSpace(string(out)); got != `"cloud-hypervisor"` {
		t.Errorf("default hypervisor = %s, want \"cloud-hypervisor\"", got)
	}
}
