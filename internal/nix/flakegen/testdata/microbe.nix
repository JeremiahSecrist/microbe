# Canonical networking test fixture for microbe's nix-eval integration tests.
#
# Note: renderer.nix reads addressing from generated.json (gen.macs/gen.addr/
# gen.gateway), not from this file's networks blocks -- the network/ip
# attrs below exist only so the file is valid microbe.nix syntax the flake
# can import for volumes/hypervisor/config; actual IPv6 addresses come from
# the Go-side fixtureConfig() driving mustStack() in the accompanying _test.go.
{
  name = "test-net";

  networks = {
    frontend = { };
    backend  = { };
  };

  services = {
    db = {
      vcpu = 1;
      mem  = 512;

      config = { pkgs, ... }: {
        services.postgresql = {
          enable = true;
          enableTCPIP = true;
          authentication = ''
            host all all ::0/0 trust
          '';
        };
      };

      volumes = [
        { type = "disk"; name = "db-data"; target = "/var/lib/postgresql"; size = "2G"; }
      ];

      networks = [
        { name = "backend"; }
      ];

      ports = [ "5432:5432" ];
    };

    web = {
      vcpu = 1;
      mem  = 512;

      config = { pkgs, ... }: {
        services.httpd.enable = true;
      };

      dependsOn = [ "db" ];

      networks = [
        { name = "backend";  }
        { name = "frontend"; }
      ];

      ports = [ "8080:80" ];
    };

    jump = {
      config = { ... }: {
        services.openssh.enable = true;
      };

      networks = [
        { name = "frontend"; }
        { name = "backend"; }
      ];
    };
  };
}
