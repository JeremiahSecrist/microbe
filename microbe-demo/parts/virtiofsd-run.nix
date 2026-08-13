# virtiofsd-run.nix — fixed flake-parts module shipped with microbe.
#
# Self-registers as flake.nixosModules.virtiofsd-run (Dendritic pattern),
# pulled in by per-service part files via config.flake.nixosModules.virtiofsd-run.
#
# Overrides microvm.nix's own microvm.binScripts.virtiofsd-run
# (nixos-modules/microvm/virtiofsd/default.nix upstream) via lib.mkForce:
# their generated supervisord config hardcodes a root `user` directive,
# which makes supervisord refuse to start ("Can't drop privilege as
# nonroot user") when
# launched unprivileged -- exactly how microbe launches every process
# (internal/runtime.startBin, no sudo). virtiofsd itself already runs fine
# unprivileged (it only skips an --rlimit-nofile bump when non-root); the
# root requirement is purely supervisord's own defensive check, not a real
# functional need. This regenerates the same supervisord-wrapped
# virtiofsd-run script without that line, and without upstream's
# systemd-notify eventlistener (pure systemd-readiness integration microbe
# doesn't use -- it already polls the socket itself, see
# internal/runtime.WaitForSocket), so there's no need to vendor their
# supervisord-event-handler.py helper either.
{
  flake.nixosModules.virtiofsd-run = { config, lib, pkgs, ... }:
    let
      virtiofsShares = builtins.filter ({ proto, ... }: proto == "virtiofs") config.microvm.shares;
    in
    lib.mkIf (virtiofsShares != []) {
      microvm.binScripts.virtiofsd-run = lib.mkForce (
        let
          supervisordConfig = { supervisord = { nodaemon = true; }; }
            // builtins.listToAttrs (map ({ tag, socket, readOnly, cache, posixAcl, extraArgs, ... }: {
              name = "program:virtiofsd-${tag}";
              value = {
                stderr_syslog = true;
                stdout_syslog = true;
                command = pkgs.writeShellScript "virtiofsd-${tag}" ''
                  exec ${lib.getExe config.microvm.virtiofsd.package} \
                    --socket-path=${lib.escapeShellArg socket} \
                    ${lib.optionalString (config.microvm.virtiofsd.group != null)
                      "--socket-group=${config.microvm.virtiofsd.group}"} \
                    --shared-dir="''${MICROBE_SHARE_${lib.replaceStrings ["-"] ["_"] (lib.toUpper tag)}}" \
                    --thread-pool-size ${toString config.microvm.virtiofsd.threadPoolSize} \
                    ${lib.optionalString posixAcl "--posix-acl --xattr"} \
                    --cache=${cache} \
                    ${lib.optionalString (config.microvm.virtiofsd.inodeFileHandles != null)
                      "--inode-file-handles=${config.microvm.virtiofsd.inodeFileHandles}"} \
                    ${lib.optionalString (config.microvm.hypervisor == "crosvm") "--tag=${tag}"} \
                    ${lib.optionalString readOnly "--readonly"} \
                    ${lib.concatStringsSep " " config.microvm.virtiofsd.extraArgs} \
                    ${lib.concatStringsSep " " extraArgs}
                '';
              };
            }) virtiofsShares);
          supervisordConfigFile = pkgs.writeText "${config.networking.hostName}-virtiofsd-supervisord.conf"
            (lib.generators.toINI {} supervisordConfig);
        in
        ''exec ${lib.getExe' pkgs.python3Packages.supervisor "supervisord"} --configuration ${supervisordConfigFile} "$@"''
      );
    };
}
