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

    proxy = {
      os = "finix";
      vcpu = 1;
      mem = 256;

      config = { pkgs, ... }:
        let
          nginxConf = pkgs.writeText "nginx.conf" ''
            user root;
            worker_processes 1;
            pid /tmp/nginx.pid;
            error_log /dev/stderr;
            events { worker_connections 64; }
            http {
              access_log off;
              client_body_temp_path /tmp;
              proxy_temp_path /tmp;
              fastcgi_temp_path /tmp;
              uwsgi_temp_path /tmp;
              scgi_temp_path /tmp;
              server {
                listen 80;
                location / {
                  proxy_pass http://192.168.50.3;
                  proxy_set_header Host $host;
                }
              }
            }
          '';
          nginxRun = pkgs.writeShellScript "nginx-run" ''
            exec ${pkgs.nginx}/bin/nginx -c ${nginxConf} -g 'daemon off;'
          '';
        in {
          finit.services.nginx = {
            command = "${nginxRun}";
            respawn = true;
            restart_sec = 2;
          };
        };

      networks = [
        { name = "frontend"; ip = "192.168.50.4"; }
      ];

      ports = [ "8090:80" ];

      dependsOn = [ "web" ];
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
