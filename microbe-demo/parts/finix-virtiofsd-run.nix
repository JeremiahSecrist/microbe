# finix-virtiofsd-run.nix — fixed flake-parts module shipped with microbe.
#
# finix's own analogue of virtiofsd-run.nix: builds a bin/virtiofsd-run
# script that runtime.StartVirtiofsd (internal/runtime/runner.go) execs
# before the VM itself. Mirrors virtiofsd-run.nix's supervisord-
# multiplexing pattern and its reasoning (unprivileged launch -- no root
# `user` directive supervisord would otherwise demand; no systemd-notify
# eventlistener -- microbe already polls the socket itself via
# internal/runtime.WaitForSocket) verbatim, just built from
# config.microbe.virtiofsShares (finix-base.nix) instead of
# config.microvm.shares (microvm.nix has no finix equivalent).
#
# Unlike nixos guests, which only need virtiofsd when a user declares a
# share volume, finix guests always need it -- config.microbe.virtiofsShares
# always includes the mandatory /nix/store share (see finix-base.nix's
# module comment for why finix diverges from nixos's baked-disk-image
# default here).
#
# Self-registers as flake.nixosModules.finix-virtiofsd-run (Dendritic
# pattern), pulled in by ServicePart's finix branch via
# config.flake.nixosModules.finix-virtiofsd-run.
{
  flake.nixosModules.finix-virtiofsd-run = { config, lib, pkgs, ... }:
    let
      svcName = config.microbe.finix.svcName;
      shares = config.microbe.virtiofsShares;

      # <rundir>/<svcName>-virtiofs-<tag>.sock -- must match finix-base.nix's
      # own virtiofsSocket naming exactly (that's what --fs tag=...,socket=
      # points at) and internal/cmd/lifecycle.go's virtiofsShareSockets
      # (what up.go's waitForSocket waits on before starting the VM).
      socketFor = tag: "${svcName}-virtiofs-${tag}.sock";

      supervisordConfig = { supervisord = { nodaemon = true; }; }
        // builtins.listToAttrs (map (s: {
          name = "program:virtiofsd-${s.tag}";
          value = {
            stderr_syslog = true;
            stdout_syslog = true;
            command = pkgs.writeShellScript "virtiofsd-${s.tag}" ''
              exec ${lib.getExe pkgs.virtiofsd} \
                --socket-path=${lib.escapeShellArg (socketFor s.tag)} \
                --shared-dir="''${MICROBE_SHARE_${lib.replaceStrings ["-"] ["_"] (lib.toUpper s.tag)}}" \
                --thread-pool-size "$(nproc)" \
                --cache=${if s.readOnly then "always" else "auto"} \
                ${lib.optionalString s.readOnly "--readonly"}
            '';
          };
        }) shares);
      supervisordConfigFile = pkgs.writeText "${svcName}-virtiofsd-supervisord.conf"
        (lib.generators.toINI { } supervisordConfig);
    in
    {
      config = lib.mkIf (shares != [ ]) {
        microbe.extraRunnerBins = [
          (pkgs.writeShellScriptBin "virtiofsd-run"
            ''exec ${lib.getExe' pkgs.python3Packages.supervisor "supervisord"} --configuration ${supervisordConfigFile} "$@"'')
        ];
      };
    };
}
