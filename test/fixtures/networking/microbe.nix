# Canonical networking test fixture for microbe.
#
# Target behavior (the spec this test suite is committed to):
#   - db    on backend  with static addr fd00:1234:5678::2, published port 5432
#   - web   on backend+frontend, one shared static addr fd00:1234:5678::3,
#           starts only after db (dependsOn)
#   - jump  on frontend+backend with NO static addr -> auto-allocated a
#           random /128 within the host's persisted ULA /64
#   - every service/interface gets a unique locally-administered MAC
#   - rules: web -> db on port 5432 is the only allowed cross-service
#     reachability; jump -> db must be blocked (default-deny)
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
        { name = "backend"; addr = "fd00:1234:5678::2"; }
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
        { name = "backend";  addr = "fd00:1234:5678::3"; }
        { name = "frontend"; addr = "fd00:1234:5678::3"; }
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

  rules = [
    { from = "web"; to = "db"; ports = [ 5432 ]; }
  ];
}
