# Canonical networking test fixture for microbe.
#
# Target behavior (the spec this test suite is committed to):
#   - db    on backend  with static IP 192.168.51.2, no published port
#           (sqlite: a mounted disk, no network listener)
#   - web   on backend+frontend with static IPs 192.168.51.3 / 192.168.50.3,
#           starts only after db (dependsOn)
#   - jump  on frontend+backend with NO static IPs -> auto-allocated
#           to the next free host: 192.168.50.2 (frontend), 192.168.51.4 (backend)
#   - every service/interface gets a unique locally-administered MAC
#   - the rendered networkd unit for db matches MAC 02:00:00:00:00:01
#     with address 192.168.51.2/24 and gateway 192.168.51.1
{
  name = "test-net";

  networks = {
    frontend = { subnet = "192.168.50.0/24"; };
    backend  = { subnet = "192.168.51.0/24"; };
  };

  services = {
    db = {
      vcpu = 1;
      mem  = 512;

      config = { pkgs, ... }: {
        environment.systemPackages = [ pkgs.sqlite ];
      };

      volumes = [
        { type = "disk"; name = "db-data"; target = "/var/lib/db"; size = "2G"; }
      ];

      networks = [
        { name = "backend"; ip = "192.168.51.2"; }
      ];
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

      networks = [
        { name = "frontend"; }
        { name = "backend"; }
      ];
    };
  };
}
