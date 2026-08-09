{
  name = "test-net";

  networks = {
    frontend = { subnet = "192.168.50.0/24"; };
    backend  = { subnet = "192.168.51.0/24"; };
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
          authentication = ''
            host all all 192.168.51.0/24 trust
          '';
        };
      };

      # Folder-bind (virtiofs, the default volume type) instead of a qcow2
      # disk -- no size to manage, host-visible files, no root needed.
      # owner: postgres needs to actually own its StateDirectory; virtiofsd
      # translates guest uid 71 (postgres) <-> whatever uid owns `host` on
      # this machine.
      volumes = [
        { name = "db-data"; host = "/home/sky/Documents/code/nix/iso/microbe-demo/pgdata"; target = "/var/lib/postgresql"; owner = "postgres"; }
      ];

      networks = [
        { name = "backend"; ip = "192.168.51.2"; }
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
        { name = "backend";  ip = "192.168.51.3"; }
        { name = "frontend"; ip = "192.168.50.3"; }
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
        { name = "notes"; host = "/home/sky/Documents/code/nix/iso/microbe-demo/shared"; target = "/shared"; }
      ];

      networks = [
        { name = "frontend"; }
        { name = "backend"; }
      ];
    };
  };
}
