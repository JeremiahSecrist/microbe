# microbe

Docker-compose-style orchestration for [microvm.nix](https://github.com/astro/microvm.nix). Define a stack of VMs in one `microbe.nix`, then `microbe up`.

## Install

Requires [Nix](https://nixos.org/download) with flakes enabled.

```sh
nix build github:JeremiahSecrist/microbe#microbe
./result/bin/microbe --version
```

Or add it to a NixOS host module:

```nix
{
  inputs.microbe.url = "github:JeremiahSecrist/microbe";

  # in your host config:
  virtualisation.microbe.enable = true;
  virtualisation.microbe.package = inputs.microbe.packages.${system}.microbe;
}
```

## Usage

Scaffold a starter stack in the current directory:

```sh
microbe init
```

This writes a `microbe.nix` like:

```nix
{
  name = "myapp";

  networks = {
    backend = { subnet = "192.168.60.0/24"; };
  };

  services = {
    web = {
      vcpu = 1;
      mem  = 512;

      config = { pkgs, ... }: {
        services.httpd.enable = true;
      };

      networks = [
        { name = "backend"; ip = "192.168.60.2"; }
      ];

      ports = [ "8080:80" ];
    };
  };
}
```

Bring the stack up:

```sh
microbe up
```

Other commands:

```sh
microbe ps                  # list services, status, IPs and ports
microbe logs [services...]  # show guest logs
microbe exec <service> [cmd...]  # run a command inside a service
microbe shell <service>     # interactive shell in a service
microbe down [services...]  # stop services, tear down host resources
microbe rm [services...]    # remove disks and state for services
microbe purge               # remove stale VMs, networks and volumes host-wide
microbe config               # print the evaluated, validated config
microbe build [services...]  # render flake and build runner derivations without starting
```
