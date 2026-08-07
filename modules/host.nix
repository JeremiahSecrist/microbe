# NixOS host module for the microbe VM orchestrator: the kernel modules,
# sysctls, tools, daemon and device access a host needs to run microvm VMs
# managed by microbe.
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
        nftables DNAT that publishes VM ports), the `microbe-provisiond` root
        daemon and its `root:microbe` mode-`0660` unix socket, and
        `microbe`/`kvm` groups with udev rules granting device access to the
        users listed in {option}`virtualisation.microbe.users`.
      '';
    };

    package = mkOption {
      type = types.nullOr types.package;
      default = null;
      description = ''
        The microbe CLI to add to {option}`environment.systemPackages` and run
        as the provisioning daemon. The microbe flake sets this to its built
        package automatically; leave unset to configure the host without the
        daemon.
      '';
    };

    users = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [ "alice" ];
      description = ''
        Users granted tap-device and KVM accelerator access (members of the
        `microbe` and `kvm` groups). Root always has access. Network
        provisioning itself runs inside the root `microbe-provisiond` daemon;
        group members drive it through the unix socket and get no shell-level
        privilege (no sudoers, no setuid).
      '';
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      boot.kernelModules = [
        "tun" # tap interfaces backing VM NICs
        "br_netfilter" # let nftables see bridged traffic (DNAT on bridges)
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
        # docker.nix pins these two at priority 98 too, so mkOverride 98 would
        # hard-collide with an enabled docker module. mkDefault defers to
        # docker's (identical) setting and still defaults to true standalone.
        "net.ipv4.conf.all.forwarding" = mkDefault true;
        "net.ipv4.conf.default.forwarding" = mkDefault true;
        "net.bridge.bridge-nf-call-iptables" = mkOverride 98 true;
        "net.bridge.bridge-nf-call-ip6tables" = mkOverride 98 true;
      };

      environment.systemPackages = [
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

      # The provisioning daemon. Mirrors systemd.sockets.docker / dockerd:
      # systemd owns the socket file (root:microbe, mode 0660) so microbe
      # group members can drive provisioning without shell-level privilege,
      # and socket-activates the root daemon which applies bridge/tap/DNAT
      # ops itself via netlink. The daemon adopts the socket fd via LISTEN_FDS
      # (`microbe provisiond`), never shelling out to ip/iptables.
      systemd.sockets.microbe-provisiond = mkIf (cfg.package != null) {
        description = "microbe provisioning daemon socket";
        wantedBy = [ "sockets.target" ];
        socketConfig = {
          ListenStream = "/run/microbe.sock";
          SocketMode = "0660";
          SocketUser = "root";
          SocketGroup = "microbe";
        };
      };

      systemd.services.microbe-provisiond = mkIf (cfg.package != null) {
        description = "microbe provisioning daemon";
        serviceConfig = {
          Type = "simple";
          ExecStart = "${cfg.package}/bin/microbe provisiond";
        };
      };
    }
  ]);
}
