# finix-network.nix — fixed flake-parts module shipped with microbe.
#
# finix's own analogue of renderer.nix's `microvm.interfaces`/
# `systemd.network.networks` pair: nixos guests get their tap NICs and
# static IP config for free from microvm.nix + systemd-networkd, but finix
# guests boot under plain QEMU (finix-base.nix) with no NIC on the command
# line at all (verified live: a finix guest built without this module has
# zero -nic/-netdev args), and finit has no systemd-networkd equivalent to
# configure one even if it existed.
#
# Two things, both driven by the same generated.json data renderer.nix
# already reads for nixos guests:
#   - `virtualisation.qemu.nics`: one pre-created tap per attached network
#     (gen.taps, same host-side tap microbe's provisiond already created
#     for this service -- host-level tap creation doesn't depend on guest
#     OS), matched to the guest NIC by MAC (gen.macs) exactly like nixos'
#     `interfaces` list.
#   - a finit task that assigns each interface its static address + routes
#     (gen.networkd -- the same per-link systemd-networkd data renderer.nix
#     writes for nixos, reused as-is since it already carries the
#     MAC/address/route triple finit needs) by MAC lookup under
#     /sys/class/net, since interface naming order isn't guaranteed to
#     match `-nic` declaration order the way systemd-networkd's own
#     MACAddress match doesn't care about naming at all.
#
# Self-registers as flake.nixosModules.finix-network (Dendritic pattern),
# pulled in by ServicePart's finix branch via
# config.flake.nixosModules.finix-network.
{
  flake.nixosModules.finix-network = { config, lib, pkgs, ... }:
    let
      generated = builtins.fromJSON (builtins.readFile ../generated.json);
      svcName = config.microCompose.serviceName;
      gen = generated.services.${svcName}
        or (throw "microbe: no generated data for service '${svcName}'");

      routeCmd = iface: r:
        if r ? Destination
        then "${pkgs.iproute2}/bin/ip route add ${r.Destination} via ${r.Gateway} dev \"${iface}\""
        else "${pkgs.iproute2}/bin/ip route add default via ${r.Gateway} dev \"${iface}\"";

      linkScript = link:
        # /sys/class/net/*/address is lowercase hex; gen.networkd's
        # MACAddress comes from the same Go-generated string as gen.macs
        # (stack.go), already lowercase -- no case-fold needed.
        ''
          mac="${link.matchConfig.MACAddress}"
          iface=""
          tries=50
          i=0
          while [ "$i" -lt "$tries" ]; do
            for p in /sys/class/net/*/address; do
              if [ "$(cat "$p")" = "$mac" ]; then
                iface=$(basename "$(dirname "$p")")
                break 2
              fi
            done
            i=$((i + 1))
            sleep 0.2
          done
          if [ -z "$iface" ]; then
            printf 'finix-network: no interface with mac %s\n' "$mac" >&2
            exit 1
          fi
          ${pkgs.iproute2}/bin/ip link set "$iface" up
          ${pkgs.iproute2}/bin/ip addr add ${lib.head link.address} dev "$iface"
        '' + lib.concatMapStringsSep "\n" (routeCmd "$iface") link.routes;

      netScript = pkgs.writeShellScript "finix-network-setup"
        (lib.concatStringsSep "\n" (lib.mapAttrsToList (_: linkScript) gen.networkd));
    in
    {
      config = {
        virtualisation.qemu.nics = lib.mapAttrs (net: tap: {
          args = [
            "tap"
            "ifname=${tap}"
            "script=no"
            "downscript=no"
            "model=virtio-net-pci"
            "mac=${gen.macs.${net}}"
          ];
        }) gen.taps;

        finit.tasks.finix-network-setup = {
          command = netScript;
          remain = true;
        };
      };
    };
}
