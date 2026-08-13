# finix-network.nix — fixed flake-parts module shipped with microbe.
#
# finix's own analogue of renderer.nix's `microvm.interfaces`/
# `systemd.network.networks` pair: nixos guests get their tap NICs and
# static IP config for free from microvm.nix + systemd-networkd, but finix
# guests get no NIC by default under microbe's own cloud-hypervisor
# invocation (finix-base.nix) without this module, and finit has no
# systemd-networkd equivalent to configure one even if it existed.
#
# Two things, both driven by the same generated.json data renderer.nix
# already reads for nixos guests:
#   - `microbe.netInterfaces` (declared by finix-base.nix, consumed by its
#     cloud-hypervisor `--net tap=...,mac=...` args): one pre-created tap
#     per attached network (gen.taps, same host-side tap microbe's
#     provisiond already created for this service -- host-level tap
#     creation doesn't depend on guest OS), matched to the guest NIC by MAC
#     (gen.macs) exactly like nixos' `interfaces` list.
#   - a finit task that assigns each interface its static address + routes
#     (gen.networkd -- the same per-link systemd-networkd data renderer.nix
#     writes for nixos, reused as-is since it already carries the
#     MAC/address/route triple finit needs) by MAC lookup under
#     /sys/class/net, since interface naming order isn't guaranteed to
#     match `--net` declaration order the way systemd-networkd's own
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
        then "${pkgs.iproute2}/bin/ip -6 route add ${r.Destination} via ${r.Gateway} dev \"${iface}\""
        else "${pkgs.iproute2}/bin/ip -6 route add default via ${r.Gateway} dev \"${iface}\"";

      linkScript = link:
        # /sys/class/net/*/address is lowercase hex; gen.networkd's
        # MACAddress comes from the same Go-generated string as gen.macs
        # (stack.go), already lowercase -- no case-fold needed.
        #
        # Tight iteration loop (no sleep): virtio-net is built-in so the
        # interface appears within milliseconds of kernel boot -- the loop
        # exits on the first or second iteration in practice. 1000 iterations
        # bounds the wait without adding fixed latency the way sleep 0.2 did
        # (each sleep was a guaranteed 200ms penalty per retry).
        ''
          mac="${link.matchConfig.MACAddress}"
          iface=""
          i=0
          while [ "$i" -lt 1000 ]; do
            for p in /sys/class/net/*/address; do
              if [ "$(cat "$p")" = "$mac" ]; then
                iface=$(basename "$(dirname "$p")")
                break 2
              fi
            done
            i=$((i + 1))
          done
          if [ -z "$iface" ]; then
            printf 'finix-network: no interface with mac %s found after 1000 tries\n' "$mac" >&2
            exit 1
          fi
          ${pkgs.iproute2}/bin/ip link set "$iface" up
          ${pkgs.iproute2}/bin/ip -6 addr add ${lib.head link.address} dev "$iface"
        '' + lib.concatMapStringsSep "\n" (routeCmd "$iface") link.routes;

      netScript = pkgs.writeShellScript "finix-network-setup"
        (lib.concatStringsSep "\n" (lib.mapAttrsToList (_: linkScript) gen.networkd));
    in
    {
      config = {
        microbe.netInterfaces = lib.mapAttrsToList (net: tap: {
          id = tap;
          mac = gen.macs.${net};
        }) gen.taps;

        finit.tasks.finix-network-setup = {
          command = netScript;
          remain = true;
        };
      };
    };
}
