# NixOS host module for the microbe VM orchestrator: the kernel modules,
# sysctls, tools and device access a host needs to run microvm VMs managed by
# microbe.
#
# Usage from the microbe flake:
#   { modules = [ microbe.nixosModules.host { virtualisation.microbe.enable = true; } ]; }
#
# Modeled after nixpkgs' virtualisation/docker.nix.
{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.virtualisation.microbe;
in
{
  options.virtualisation.microbe = {
    enable = mkOption {
      type = types.bool;
      default = false;
      description = ''
        Enable host support for the microbe VM orchestrator: tap/bridge kernel
        modules, IP forwarding + bridge netfilter sysctls (required for the
        iptables DNAT that publishes VM ports), the tools microbe shells out to
        (qemu-img, iptables, iproute2), and `microbe`/`kvm` groups with udev
        rules granting device access to the users listed in
        {option}`virtualisation.microbe.users`.
      '';
    };

    package = mkOption {
      type = types.nullOr types.package;
      default = null;
      description = ''
        The microbe CLI to add to {option}`environment.systemPackages`. The
        microbe flake sets this to its built package automatically; leave unset
        to configure the host without installing the CLI.
      '';
    };

    users = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [ "alice" ];
      description = ''
        Users granted tap-device and KVM accelerator access (members of the
        `microbe` and `kvm` groups). Root always has access. Bridge, tap and
        iptables provisioning itself still requires root (e.g. passwordless
        sudo); the groups only remove the need for a privileged helper on
        device nodes.
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      boot.kernelModules = [
        "tun" # tap interfaces backing VM NICs
        "br_netfilter" # let iptables see bridged traffic (DNAT on bridges)
        "vhost" # virtio-net acceleration
        "vhost_net"
        # Loaded when the CPU supports them; modprobe warns harmlessly
        # otherwise (e.g. booting the ISO on hardware without the feature).
        "kvm_intel"
        "kvm_amd"
      ];

      boot.kernel.sysctl = {
        # Priority 98 (as docker does) so user config can override these.
        "net.ipv4.ip_forward" = mkOverride 98 true;
        "net.ipv4.conf.all.forwarding" = mkOverride 98 true;
        "net.ipv4.conf.default.forwarding" = mkOverride 98 true;
        "net.bridge.bridge-nf-call-iptables" = mkOverride 98 true;
        "net.bridge.bridge-nf-call-ip6tables" = mkOverride 98 true;
      };

      environment.systemPackages = [
        pkgs.iproute2 # ip link / bridge / tap management
        pkgs.iptables # DNAT for published VM ports
        pkgs.qemu-utils # qemu-img for VM volume images
      ] ++ optionals (cfg.package != null) [ cfg.package ];

      users.groups.microbe = { };
      users.groups.kvm = { };
      users.users = genAttrs cfg.users (name: {
        extraGroups = [ "microbe" "kvm" ];
      });

      services.udev.extraRules = ''
        # Tap devices for the microbe group, KVM accelerator for the kvm group.
        KERNEL=="tun", GROUP="microbe", MODE="0660", OPTIONS+="static_node=net/tun"
        KERNEL=="kvm", GROUP="kvm", MODE="0660", OPTIONS+="static_node=kvm"
      '';
    }
  ]);
}
