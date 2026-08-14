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

    nat64 = {
      enable = mkOption {
        type = types.bool;
        default = cfg.enable;
        description = ''
          Give guests (IPv6-only; see the flat-network addressing model)
          outbound internet access via NAT64 (tayga) + DNS64 (unbound):
          guest apps resolving an IPv4-only hostname get an
          A-record-synthesized AAAA answer in the well-known
          {option}`64:ff9b::/96` prefix, and tayga translates the resulting
          v6 flow to v4 on the host's own uplink. Reachability *to* a
          published port from an IPv6-capable external client needs no
          NAT64 at all (microbe-provisiond DNATs straight to the guest's
          real IPv6 address); reachability from an IPv4-only external
          client is not yet implemented (tracked separately -- tayga's
          static `mappings` are rendered once at nix-eval time, and
          microbe publishes/unpublishes ports at `up`/`down` time, so a
          static nix-declared mapping table can't track that without a
          `nixos-rebuild` per port change).
        '';
      };

      ipv4Pool = mkOption {
        type = types.str;
        default = "100.64.1.0/24";
        description = ''
          The IPv4 address pool tayga hands each translated IPv6 flow a
          temporary address from (RFC 6598 CGNAT space by default, so it
          won't collide with a typical host's own LAN/uplink addressing).
        '';
      };
    };
  };

  config = mkIf cfg.enable (mkMerge [
    {
      boot.kernelModules = [
        "tun" # tap interfaces backing VM NICs
        "br_netfilter" # let nftables see bridged traffic (DNAT on bridges)
        "vhost" # virtio-net acceleration
        "vhost_net"
        "vhost_vsock" # AF_VSOCK transport for `microbe shell`/`exec` into finix guests
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
        # Guests are IPv6-only (flat-network model): the host must forward
        # between stack bridges (and, with nat64.enable, into the tayga tun
        # device) regardless of whether NAT64 itself is turned on.
        "net.ipv6.conf.all.forwarding" = mkOverride 98 true;
        "net.ipv6.conf.default.forwarding" = mkDefault true;
      };

      environment.systemPackages = [
        pkgs.qemu-utils # qemu-img for VM volume images
        pkgs.e2fsprogs # mkfs.ext4 to format them (EnsureVolume, unprivileged)
      ] ++ optionals (cfg.package != null) [ cfg.package ];

      users.groups.microbe = { };
      users.groups.kvm = { };
      users.users = genAttrs cfg.users (name: {
        extraGroups = [ "microbe" "kvm" ];
      });

      # Daemon-owned data dir (state.json, volumes, logs, VM run sockets),
      # one subdir per stack name — mirrors /var/lib/docker.
      # Setgid + group microbe so unprivileged group members can create
      # their stack's subdir themselves; no daemon round-trip needed for
      # plain file I/O, matching the existing group-membership trust model.
      systemd.tmpfiles.rules = [ "d /var/lib/microbe 2775 root microbe -" ];

      services.udev.extraRules = ''
        # tun left at the Linux default (0666, world-writable): nix's own
        # build sandbox opens /dev/net/tun itself (via pasta) to give
        # fixed-output derivations network access, and restricting it to
        # the microbe group blocks nix's nixbld users from doing that on
        # any host running this module. Tap creation is still gated by the
        # root-only provisiond daemon (see below), not by this device's
        # permissions, so loosening it doesn't widen microbe's own attack
        # surface.
        KERNEL=="tun", OPTIONS+="static_node=net/tun"
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
    (mkIf cfg.nat64.enable {
      # Outbound-only NAT64: guest (IPv6-only) -> tayga tun -> host's own
      # IPv4 uplink. ipv6.pool is the well-known RFC 6052 prefix dns64
      # rewrites A-only answers into (see the unbound config below);
      # ipv4.pool is where each translated flow gets a temporary source
      # address. wkpfStrict is off because ipv4Pool is deliberately
      # non-global (RFC 6598) -- the well-known prefix's own RFC 6052
      # restriction against translating non-global v4 ranges doesn't apply
      # to what is, here, entirely host-internal traffic.
      services.tayga = {
        enable = true;
        wkpfStrict = false;
        ipv4 = {
          address = "100.64.0.1";
          router.address = "100.64.0.2";
          pool = {
            address = builtins.elemAt (lib.splitString "/" cfg.nat64.ipv4Pool) 0;
            prefixLength = lib.toInt (builtins.elemAt (lib.splitString "/" cfg.nat64.ipv4Pool) 1);
          };
        };
        ipv6.router.address = "64:ff9b::1";
        ipv6.pool = {
          address = "64:ff9b::";
          prefixLength = 96;
        };
      };

      # dns64: rewrites an A-only answer into the NAT64 prefix so a guest
      # resolving an IPv4-only hostname still gets something it can dial
      # (tayga then does the actual v6<->v4 translation). access-control is
      # fd00::/8 -- every host ULA prefix microbe ever generates (see
      # internal/provisiond/prefix.go) falls under RFC 4193's
      # locally-assigned range, so this doesn't need to know this
      # particular host's actual generated prefix.
      services.unbound = {
        enable = true;
        settings.server = {
          interface = [ "::0" ];
          access-control = [ "fd00::/8 allow" ];
          module-config = "\"dns64 validator iterator\"";
          dns64-prefix = "64:ff9b::/96";
        };
      };
    })
  ]);
}
