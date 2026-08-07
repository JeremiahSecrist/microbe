# renderer.nix — fixed module shipped with microbe.
#
# Maps a compose service onto microvm.nix options. Reads:
#   - ../microbe.nix    the user's compose file (native render view)
#   - ../generated.nix  CLI-assigned cid/macs/ips/hosts/networkd (spec 9.2)
# The per-service module modules/<svc>.nix sets microCompose.serviceName so
# this module knows which slice of the compose file and generated data to use.
{ config, lib, pkgs, ... }:

let
  compose = import ../microbe.nix;
  generated = import ../generated.nix;

  svcName = config.microCompose.serviceName;
  svc = compose.services.${svcName}
    or (throw "microbe: no service '${svcName}' in compose file");
  gen = generated.services.${svcName}
    or (throw "microbe: no generated data for service '${svcName}'");

  # "2G" -> 2048 MiB (units are MiB-relative; microvm volume sizes are MiB).
  # Bare numbers are MiB; sub-MiB units (k/b) are rejected.
  sizeToMiB = size:
    let
      s = lib.toLower size;
      m = builtins.match "([0-9]+)([kmgt]?i?b?)" s;
      num = builtins.fromJSON (builtins.elemAt m 0);
      unit = builtins.elemAt m 1;
      scale = {
        m = 1; mb = 1; mib = 1;
        g = 1024; gb = 1024; gib = 1024;
        t = 1024 * 1024; tb = 1024 * 1024; tib = 1024 * 1024;
      };
    in
    if m == null then
      throw "microbe: service '${svcName}': invalid volume size '${size}'"
    else if unit == "" then
      num
    else
      num * (scale.${unit}
        or (throw "microbe: service '${svcName}': unsupported volume size unit '${unit}' in '${size}' (use M, G, or T)"));

  volumes = lib.optionals (svc ? volumes) (map (v: {
    image = gen.volumes.${v.name}.image
      or (throw "microbe: service '${svcName}': no generated image path for volume '${v.name}'");
    mountPoint = v.target;
    size = sizeToMiB (v.size or (throw "microbe: service '${svcName}': disk '${v.name}' needs a size"));
    fsType = v.fsType or "ext4";
    imageType = "raw";
    autoCreate = false;
  }) (builtins.filter (v: v.type == "disk") svc.volumes));

  shares = lib.optionals (svc ? volumes) (map (v: {
    tag = v.name;
    source = v.host or (throw "microbe: service '${svcName}': share '${v.name}' needs a host path");
    mountPoint = v.target;
    proto = v.protocol or "9p";
    readOnly = v.mode == "ro";
  }) (builtins.filter (v: v.type == "share") svc.volumes));

  # One tap interface per attached network; id is the host-side tap name the
  # CLI creates (spec 8.2), resolved from generated.nix so both sides agree.
  interfaces = lib.mapAttrsToList (net: mac: {
    type = "tap";
    id = gen.taps.${net}
      or (throw "microbe: service '${svcName}': no tap id for network '${net}'");
    inherit mac;
  }) gen.macs;

  hostsText = (lib.concatMapStringsSep "\n"
    (h: "${h.ip} ${lib.concatStringsSep " " h.names}")
    gen.hosts) + "\n";
in
{
  options.microCompose.serviceName = lib.mkOption {
    type = lib.types.str;
    description = "Service this configuration renders; set by modules/<svc>.nix.";
  };

  config = {
    networking.hostName = svcName;
    networking.useDHCP = lib.mkDefault false;
    networking.nameservers = lib.mkDefault [ "1.1.1.1" ];

    microvm = {
      hypervisor = lib.mkDefault (svc.hypervisor or "cloud-hypervisor");
      vcpu = lib.mkDefault (svc.vcpu or 1);
      mem = lib.mkDefault (svc.mem or 512);
      vsock.cid = gen.cid;
      inherit interfaces volumes shares;
    };

    systemd.network.enable = lib.mkDefault true;
    systemd.network.networks = gen.networkd;

    environment.etc.hosts = lib.mkForce { text = hostsText; };
  };
}
