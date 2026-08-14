{
  name = "test-net";

  networks = {
    frontend = { };
    backend  = { };
  };

  services = {
    db = {
      vcpu = 2;
      mem  = 512;

      config = { pkgs, ... }: {
          environment.systemPackages = [
            pkgs.fastfetch
          ];
        services.postgresql = {
          enable = true;
          enableTCPIP = true;
          # rules: (below) is the actual access boundary now -- pg_hba is
          # defense-in-depth, not primary enforcement. There's no fixed
          # subnet to trust anymore (every service shares one flat,
          # randomly-addressed /64 -- see microbe.lock.json once you've
          # run `up`), so this is deliberately wide open.
          authentication = ''
            host all all ::0/0 trust
          '';
        };
      };

      # Folder-bind (virtiofs, the default volume type) instead of a qcow2
      # disk -- no size to manage, host-visible files, no root needed.
      # owner: postgres needs to actually own its StateDirectory; virtiofsd
      # translates guest uid 71 (postgres) <-> whatever uid owns `host` on
      # this machine.
      volumes = [
        { name = "db-data"; host = "./pgdata"; target = "/var/lib/postgresql"; owner = "postgres"; }
      ];

      # No static addr: db gets a random IPv6 /128 within the host's
      # persisted ULA /64 on first `up`, permanently recorded in
      # microbe.lock.json (committed to git like a Cargo.lock) so it never
      # changes again after that.
      networks = [
        { name = "backend"; }
      ];

      ports = [ "5432:5432" ];

      # healthcheck = { port = 5432; };
    };

    web = {
      vcpu = 1;
      mem  = 512;

      config = { pkgs, ... }: {
        services.httpd.enable = true;
      };

      dependsOn = [ "db" ];

      networks = [
        { name = "backend"; }
        { name = "frontend"; }
      ];

      ports = [ "8080:80" ];
    };

    jump = {
      config = { ... }: {
        services.openssh.enable = true;
      };

      # Folder-bind volume (virtiofs), the default volume type when `type`
      # is omitted — see internal/config/load.go's applyDefaults. Runs
      # unprivileged (internal/nix/flakegen/parts/virtiofsd-run.nix).
      volumes = [
        { name = "notes"; host = "./shared"; target = "/shared"; }
      ];

      networks = [
        { name = "frontend"; }
        { name = "backend"; }
      ];
    };
  };

  # Default-deny: services can only reach each other over rules declared
  # here. web -> db is the only cross-service traffic this demo actually
  # needs (jump has no app talking to db/web, so it gets no rule).
  rules = [
    { from = "web"; to = "db"; ports = [ 5432 ]; }
  ];
}
