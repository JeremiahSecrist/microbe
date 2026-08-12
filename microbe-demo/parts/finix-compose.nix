# finix-compose.nix — fixed flake-parts module shipped with microbe.
#
# Reads the generated stack's microbe.nix and generated.json and populates
# config.microbe.finix.* so finix-base.nix has no direct file-import coupling.
# Imported by ServicePart (flake.go) for every finix service, alongside
# finix-base.nix. The split means finix-base.nix can be imported in contexts
# that don't have a compose/generated file pair (e.g. the main flake's
# standalone test VM) by setting microbe.finix.* options directly instead.
{
  flake.nixosModules.finix-compose = { config, lib, ... }:
    let
      compose = import ../microbe.nix;
      generated = builtins.fromJSON (builtins.readFile ../generated.json);

      svcName = config.microCompose.serviceName;
      svc = compose.services.${svcName}
        or (throw "microbe: no service '${svcName}' in compose file");
      gen = generated.services.${svcName}
        or (throw "microbe: no generated data for service '${svcName}'");

      # "share" is the default volume type when omitted (see config.load's
      # applyDefaults) -- mirrored here since this module imports the user's
      # raw microbe.nix directly and doesn't go through Go's defaulting.
      volumeType = v: v.type or "share";
    in
    {
      microbe.finix = {
        inherit svcName;
        vcpu = svc.vcpu or 1;
        mem = svc.mem or 512;
        cid = gen.cid;
        # User-declared share volumes -- disk-type volumes aren't wired for
        # finix yet (cloud-hypervisor `--disk` + guest-side mount is a
        # separate, unimplemented piece; only share/virtiofs is in scope
        # here). Owner-uid translation (renderer.nix's `--translate-uid`) is
        # also not yet ported -- shares mount with virtiofsd's own uid, no
        # guest-user remapping.
        userShares = lib.optionals (svc ? volumes) (map (v: {
          tag = v.name;
          source = gen.volumes.${v.name}.host
            or (throw "microbe: service '${svcName}': share '${v.name}' needs a host path");
          mountPoint = v.target;
          readOnly = (v.mode or "rw") == "ro";
        }) (builtins.filter (v: volumeType v == "share") svc.volumes or [ ]));
      };
    };
}
