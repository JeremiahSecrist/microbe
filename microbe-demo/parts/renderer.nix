# renderer.nix — fixed flake-parts module shipped with microbe.
#
# Self-registers as flake.nixosModules.renderer (Dendritic pattern):
# per-service part files (see ServicePart) pull it in via
# config.flake.nixosModules.renderer rather than an explicit path import.
# Maps a compose service onto microvm.nix options. Reads:
#   - ../microbe.nix     the user's compose file (native render view)
#   - ../generated.json  CLI-assigned cid/macs/ips/hosts/networkd (spec 9.2)
# The per-service part sets microCompose.serviceName so this module knows
# which slice of the compose file and generated data to use.
{
  flake.nixosModules.renderer = { config, lib, pkgs, ... }:
    let
      compose = import ../microbe.nix;
      generated = builtins.fromJSON (builtins.readFile ../generated.json);

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

      # "share" is the default volume type when omitted (see config.load's
      # applyDefaults) -- mirrored here since this module imports the user's
      # raw microbe.nix directly and doesn't go through Go's defaulting.
      volumeType = v: v.type or "share";

      volumes = lib.optionals (svc ? volumes) (map (v: {
        image = gen.volumes.${v.name}.image
          or (throw "microbe: service '${svcName}': no generated image path for volume '${v.name}'");
        mountPoint = v.target;
        size = sizeToMiB (v.size or (throw "microbe: service '${svcName}': disk '${v.name}' needs a size"));
        fsType = v.fsType or "ext4";
        imageType = "raw";
        autoCreate = false;
      }) (builtins.filter (v: volumeType v == "disk") svc.volumes));

      shares = lib.optionals (svc ? volumes) (map (v:
        {
          tag = v.name;
          # Host path is injected at runtime via $MICROBE_SHARE_<TAG> (set by
          # microbe up before starting virtiofsd); "placeholder" keeps the typed
          # microvm.shares option happy while leaving the derivation hash
          # host-independent.
          source = "placeholder";
          mountPoint = v.target;
          # cloud-hypervisor only supports virtiofs shares, not 9p (see
          # config.load's applyDefaults comment) -- virtiofs is the default.
          proto = v.protocol or "virtiofs";
          readOnly = (v.mode or "rw") == "ro";
        }
        # v.owner: translate this share's files to appear owned by that
        # guest user (resolved from the guest's own user database) instead
        # of whatever uid virtiofsd itself runs as -- needed for anything
        # that wants a fixed system uid to actually own its data (e.g.
        # postgres's StateDirectory=). See internal/cmd/up.go's
        # attachShareOwners for the host side of the mapping.
        //
        # guestUid/guestGid are baked in (host-independent: from the guest's
        # own user database). hostUid/hostGid are injected at runtime via
        # $MICROBE_HOST_UID_<TAG> / $MICROBE_HOST_GID_<TAG>.
        lib.optionalAttrs (v ? owner) (
          let
            dollar = "$";
            tagUpper = lib.replaceStrings ["-"] ["_"] (lib.toUpper v.name);
            guestUser = config.users.users.${v.owner}
              or (throw "microbe: service '${svcName}': share '${v.name}': no such guest user '${v.owner}' (declare it in this service's config, e.g. via the service module that owns it)");
            guestUid = guestUser.uid
              or (throw "microbe: service '${svcName}': share '${v.name}': guest user '${v.owner}' has no static uid (DynamicUser?)");
            guestGid = config.users.groups.${guestUser.group}.gid
              or (throw "microbe: service '${svcName}': share '${v.name}': guest group '${guestUser.group}' has no static gid");
          in {
            # --translate-uid/--translate-gid can't be combined with posix ACLs.
            posixAcl = false;
            extraArgs = [
              "--translate-uid" "map:${toString guestUid}:${dollar}{MICROBE_HOST_UID_${tagUpper}}:1"
              "--translate-gid" "map:${toString guestGid}:${dollar}{MICROBE_HOST_GID_${tagUpper}}:1"
            ];
          }
        )
      ) (builtins.filter (v: volumeType v == "share") svc.volumes));

      # One tap interface per attached network; id is the host-side tap name the
      # CLI creates (spec 8.2), resolved from generated.json so both sides agree.
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
        description = "Service this configuration renders; set by parts/<svc>.nix.";
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
    };
}
