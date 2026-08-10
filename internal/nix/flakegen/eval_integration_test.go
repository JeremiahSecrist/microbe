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
  networks = { backend = { subnet = "192.168.90.0/24"; }; };
  services = {
    a = {
      os = "finix";
      networks = [ { name = "backend"; ip = "192.168.90.2"; } ];
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
		Networks:      map[string]config.Network{"backend": {Subnet: "192.168.90.0/24"}},
		Services: map[string]config.Service{
			"a": {OS: "finix", Networks: []config.Attach{{Name: "backend", IP: "192.168.90.2"}}},
		},
	}
	st := mustStack(t, cfg)
	if err := WriteStack(dir, st); err != nil {
		t.Fatal(err)
	}

	target := st.Services["a"].BuildTarget
	cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", target)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("eval %s: %v\n%s", target, err, stderr.String())
	}
	// bin/microvm-run, not bin/run-vm: runtime.StartService (internal/
	// runtime/runner.go) hardcodes that binName for every service
	// regardless of OS, so finix's qemuRunner derivation must ship its
	// script under that name for `microbe up` to find it.
	if !strings.Contains(string(out), "microvm-run") {
		t.Errorf("qemuRunner = %s, want a microvm-run store path", out)
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
  networks = { backend = { subnet = "192.168.93.0/24"; }; };
  services = {
    a = {
      os = "finix";
      networks = [ { name = "backend"; ip = "192.168.93.2"; } ];
    };
    b = {
      os = "finix";
      networks = [ { name = "backend"; ip = "192.168.93.3"; } ];
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
		Networks:      map[string]config.Network{"backend": {Subnet: "192.168.93.0/24"}},
		Services: map[string]config.Service{
			"a": {OS: "finix", Networks: []config.Attach{{Name: "backend", IP: "192.168.93.2"}}},
			"b": {OS: "finix", Networks: []config.Attach{{Name: "backend", IP: "192.168.93.3"}}},
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
  networks = { backend = { subnet = "192.168.80.0/24"; }; };
  services = {
    db = {
      config = { ... }: { services.postgresql.enable = true; };
      volumes = [
        { type = "share"; name = "data"; host = "` + shareHost + `"; target = "/var/lib/postgresql"; owner = "postgres"; }
      ];
      networks = [ { name = "backend"; ip = "192.168.80.2"; } ];
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
		Networks:      map[string]config.Network{"backend": {Subnet: "192.168.80.0/24"}},
		Services: map[string]config.Service{
			"db": {Networks: []config.Attach{{Name: "backend", IP: "192.168.80.2"}}},
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
	if !strings.Contains(joined, "--translate-uid map:71:1000:1") {
		t.Errorf("extraArgs = %v, want --translate-uid map:71:1000:1 (postgres guest uid 71 -> host uid 1000)", share.ExtraArgs)
	}
	if !strings.Contains(joined, "--translate-gid map:71:100:1") {
		t.Errorf("extraArgs = %v, want --translate-gid map:71:100:1 (postgres guest gid 71 -> host gid 100)", share.ExtraArgs)
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
