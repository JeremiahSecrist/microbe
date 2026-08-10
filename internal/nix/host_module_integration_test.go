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
// must gain the kernel modules, sysctls, the microbe-provisiond daemon socket
// + service, packages and udev rules needed to run microbe VMs, and must be
// inert when disabled. Eval-only, skipped without nix.
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
      microbeSocket = on.config.systemd.sockets."microbe-provisiond".socketConfig;
      microbeSvc = on.config.systemd.services."microbe-provisiond".serviceConfig;
      # Packages the module itself adds to the system (base NixOS already ships
      # iproute2/iptables, so presence alone proves nothing).
      onPkgs = map (p: p.pname or "") on.config.environment.systemPackages;
      offPkgs = map (p: p.pname or "") off.config.environment.systemPackages;
      addedPkgs = builtins.filter (n: !builtins.elem n offPkgs) onPkgs;
      # Sudo rules that specifically target the microbe group.
      microbeSudoRules = builtins.concatLists (map (r:
        if builtins.elem "microbe" (r.groups or []) then
          map (c: { cmd = c.command; noPasswd = builtins.elem "NOPASSWD" c.options; }) r.commands
        else [ ]
      ) on.config.security.sudo.extraRules);
    in {
      eval = {
        kernelModules = on.config.boot.kernelModules;
        ipForward = on.config.boot.kernel.sysctl."net.ipv4.ip_forward";
        bridgeNf = on.config.boot.kernel.sysctl."net.bridge.bridge-nf-call-iptables";
        addedPkgs = addedPkgs;
        hasPackage = builtins.any (p: p.pname or "" == "hello") on.config.environment.systemPackages;
        microbeGroup = on.config.users.groups ? microbe;
        kvmGroup = on.config.users.groups ? kvm;
        tunRule = builtins.match ".*KERNEL==\"tun\".*" on.config.services.udev.extraRules != null;
        aliceGroups = on.config.users.users.alice.extraGroups;
        disabledGroup = off.config.users.groups ? microbe;
        dockerForwarding = withDocker.config.boot.kernel.sysctl."net.ipv4.conf.all.forwarding";
        socketListen = microbeSocket.ListenStream;
        socketMode = microbeSocket.SocketMode;
        socketUser = microbeSocket.SocketUser;
        socketGroup = microbeSocket.SocketGroup;
        socketWantedBy = on.config.systemd.sockets."microbe-provisiond".wantedBy;
        svcType = microbeSvc.Type;
        svcExecStart = microbeSvc.ExecStart;
        microbeSudoRules = microbeSudoRules;
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
		KernelModules  []string
		IPForward      any
		BridgeNf       any
		AddedPkgs      []string `json:"addedPkgs"`
		HasPackage     bool
		MicrobeGroup   bool
		KVMGroup       bool
		TunRule        bool
		AliceGroups    []string
		DisabledGroup  bool
		DockerFwd      any `json:"dockerForwarding"`
		SocketListen   string
		SocketMode     string
		SocketUser     string
		SocketGroup    string
		SocketWantedBy []string
		SvcType        string
		SvcExecStart   string
		SudoRules      []struct {
			Cmd      string `json:"cmd"`
			NoPasswd bool   `json:"noPasswd"`
		} `json:"microbeSudoRules"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode eval: %v\n%s", err, out)
	}

	for _, want := range []string{"tun", "br_netfilter", "vhost", "vhost_net", "vhost_vsock"} {
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
	if !containsStr(got.AddedPkgs, "qemu-utils") {
		t.Errorf("module-added packages missing qemu-utils (qemu-img for volumes): %v", got.AddedPkgs)
	}
	if !containsStr(got.AddedPkgs, "hello") {
		t.Errorf("virtualisation.microbe.package (hello) not added to systemPackages: %v", got.AddedPkgs)
	}
	if containsStr(got.AddedPkgs, "iptables") {
		t.Errorf("module still adds iptables; the daemon uses nftables netlink: %v", got.AddedPkgs)
	}
	if containsStr(got.AddedPkgs, "iproute2") {
		t.Errorf("module still adds iproute2; the daemon uses netlink: %v", got.AddedPkgs)
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
	if got.SocketListen != "/run/microbe.sock" {
		t.Errorf("daemon socket ListenStream = %q, want /run/microbe.sock", got.SocketListen)
	}
	if got.SocketMode != "0660" || got.SocketUser != "root" || got.SocketGroup != "microbe" {
		t.Errorf("daemon socket ownership = %s:%s %s, want root:microbe 0660", got.SocketUser, got.SocketGroup, got.SocketMode)
	}
	if !containsStr(got.SocketWantedBy, "sockets.target") {
		t.Errorf("daemon socket wantedBy = %v, want sockets.target", got.SocketWantedBy)
	}
	if got.SvcType != "simple" || !strings.Contains(got.SvcExecStart, "/bin/microbe provisiond") {
		t.Errorf("daemon service = type %q exec %q", got.SvcType, got.SvcExecStart)
	}
	if len(got.SudoRules) != 0 {
		t.Errorf("sudoers rules still present for the microbe group: %v (daemon owns provisioning)", got.SudoRules)
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
