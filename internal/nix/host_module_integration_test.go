package nix

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostModuleConfiguresHost is the red-green gate for modules/host.nix: a
// NixOS host importing the module with virtualisation.microbe.enable = true
// must gain the kernel modules, sysctls, packages, groups and udev rules
// needed to run microbe VMs, and must be inert when disabled. Eval-only,
// skipped without nix.
func TestHostModuleConfiguresHost(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH")
	}

	dir := t.TempDir()
	hostSrc, err := os.ReadFile(filepath.Join("..", "..", "modules", "host.nix"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.nix"), hostSrc, 0o644); err != nil {
		t.Fatal(err)
	}

	flake := `{
  inputs = { nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable"; };
  outputs = { nixpkgs, ... }:
    let
      on = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        modules = [
          ./host.nix
          {
            virtualisation.microbe.enable = true;
            virtualisation.microbe.users = [ "alice" ];
            virtualisation.microbe.package = nixpkgs.legacyPackages.x86_64-linux.hello;
          }
        ];
      };
      off = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        modules = [ ./host.nix ];
      };
      # docker.nix pins net.ipv4.conf.{all,default}.forwarding at priority 98;
      # microbe must coexist with it instead of hard-colliding.
      withDocker = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        modules = [
          ./host.nix
          {
            virtualisation.docker.enable = true;
            virtualisation.microbe.enable = true;
            virtualisation.microbe.package = nixpkgs.legacyPackages.x86_64-linux.hello;
          }
        ];
      };
    in {
      eval = {
        kernelModules = on.config.boot.kernelModules;
        ipForward = on.config.boot.kernel.sysctl."net.ipv4.ip_forward";
        bridgeNf = on.config.boot.kernel.sysctl."net.bridge.bridge-nf-call-iptables";
        hasQemuImg = builtins.any (p: p.pname or "" == "qemu-utils") on.config.environment.systemPackages;
        hasIptables = builtins.any (p: p.pname or "" == "iptables") on.config.environment.systemPackages;
        hasIproute2 = builtins.any (p: p.pname or "" == "iproute2") on.config.environment.systemPackages;
        hasPackage = builtins.any (p: p.pname or "" == "hello") on.config.environment.systemPackages;
        microbeGroup = on.config.users.groups ? microbe;
        kvmGroup = on.config.users.groups ? kvm;
        tunRule = builtins.match ".*KERNEL==\"tun\".*" on.config.services.udev.extraRules != null;
        aliceGroups = on.config.users.users.alice.extraGroups;
        disabledGroup = off.config.users.groups ? microbe;
        dockerForwarding = withDocker.config.boot.kernel.sysctl."net.ipv4.conf.all.forwarding";
        sudoRules = builtins.concatLists (map (r: map (c: { cmd = c.command; noPasswd = builtins.elem "NOPASSWD" c.options; }) r.commands) on.config.security.sudo.extraRules);
      };
    };
}`
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(flake), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("nix", "eval", "--json", "--no-write-lock-file", ".#eval")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("host module did not evaluate: %v\n%s", err, ee.Stderr)
		}
		t.Fatal(err)
	}

	var got struct {
		KernelModules []string
		IPForward     any
		BridgeNf      any
		HasQemuImg    bool
		HasIptables   bool
		HasIproute2   bool
		HasPackage    bool
		MicrobeGroup  bool
		KVMGroup      bool
		TunRule       bool
		AliceGroups   []string
		DisabledGroup bool
		DockerFwd     any `json:"dockerForwarding"`
		SudoRules     []struct {
			Cmd      string `json:"cmd"`
			NoPasswd bool   `json:"noPasswd"`
		} `json:"sudoRules"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode eval: %v\n%s", err, out)
	}

	for _, want := range []string{"tun", "br_netfilter", "vhost", "vhost_net"} {
		if !containsStr(got.KernelModules, want) {
			t.Errorf("boot.kernelModules missing %q: %v", want, got.KernelModules)
		}
	}
	if !isTruthy(got.IPForward) {
		t.Errorf("net.ipv4.ip_forward = %v, want true", got.IPForward)
	}
	if !isTruthy(got.BridgeNf) {
		t.Errorf("bridge-nf-call-iptables = %v, want true", got.BridgeNf)
	}
	if !got.HasQemuImg {
		t.Error("environment.systemPackages missing qemu-utils (qemu-img for volumes)")
	}
	if !got.HasIptables {
		t.Error("environment.systemPackages missing iptables (DNAT)")
	}
	if !got.HasIproute2 {
		t.Error("environment.systemPackages missing iproute2 (bridge/tap management)")
	}
	if !got.HasPackage {
		t.Error("virtualisation.microbe.package not added to environment.systemPackages")
	}
	if !got.MicrobeGroup {
		t.Error("users.groups.microbe missing when enabled")
	}
	if !got.KVMGroup {
		t.Error("users.groups.kvm missing when enabled")
	}
	if !got.TunRule {
		t.Error("udev rule granting the microbe group tun access missing")
	}
	for _, g := range []string{"microbe", "kvm"} {
		if !containsStr(got.AliceGroups, g) {
			t.Errorf("configured user alice missing %s group: %v", g, got.AliceGroups)
		}
	}
	if got.DisabledGroup {
		t.Error("microbe group present when module is disabled")
	}
	if !isTruthy(got.DockerFwd) {
		t.Errorf("net.ipv4.conf.all.forwarding with docker enabled = %v, want true (no priority conflict)", got.DockerFwd)
	}
	wantCmds := map[string]bool{"/bin/ip": false, "/bin/xtables-nft-multi": false}
	for _, r := range got.SudoRules {
		for suffix := range wantCmds {
			if strings.HasSuffix(r.Cmd, suffix) {
				wantCmds[suffix] = true
				if !r.NoPasswd {
					t.Errorf("sudoers command %s missing NOPASSWD", r.Cmd)
				}
			}
		}
	}
	for cmd, found := range wantCmds {
		if !found {
			t.Errorf("sudoers rule missing %s for the microbe group", cmd)
		}
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// isTruthy accepts Nix bool true or int 1 as JSON.
func isTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t == 1
	}
	return false
}
