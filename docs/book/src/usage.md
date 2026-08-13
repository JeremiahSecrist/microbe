# Usage

## Scaffold a stack

```sh
microbe init
```

This writes a `microbe.nix` in the current directory:

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

`init` refuses to overwrite an existing `microbe.nix`. Edit the file, then bring the stack up:

```sh
microbe up
```

`up` also writes `flake.nix`, `generated.json` and `parts/*.nix` next to `microbe.nix` — the real Nix the stack runs, rendered from what you declared. They're safe to read (or commit) and are only rewritten when their content actually changes.

Commands need membership in the `microbe` group (see [Setup](./setup.md)) — no `sudo` required once you're in it.

## A walkthrough with dependencies and health checks

A more realistic stack has multiple services with a startup dependency between them, gated on one actually being healthy rather than just having started. Take a two-service stack: `db` (a database with a healthcheck) and `web` (depends on `db`):

```nix
services = {
  db = {
    vcpu = 1;
    mem  = 512;
    config = { pkgs, ... }: { services.postgresql.enable = true; };
    networks = [ { name = "backend"; ip = "192.168.60.2"; } ];
    healthcheck = { port = 5432; startPeriod = "10s"; };
  };

  web = {
    vcpu = 1;
    mem  = 512;
    config = { pkgs, ... }: { services.httpd.enable = true; };
    networks = [ { name = "backend"; ip = "192.168.60.3"; } ];
    dependsOn = [ "db" ];
    ports = [ "8080:80" ];
  };
};
```

`microbe up` starts `db`, waits for something to actually accept connections on port 5432 within `startPeriod`, and only then starts `web`. If `db` never becomes healthy, `up` exits non-zero and `web` never starts at all — `microbe ps` will show it `stopped`, no PID. `dependsOn` gates on health, not just process-start.

You can see this directly: point `db`'s `healthcheck.port` at something nothing listens on, `microbe down --remove-volumes && microbe up`, and watch it report `degraded db: service "db" did not become healthy within 10s` while `web` stays down. Point the port back and `up` again to confirm it recovers.

## Checking on a running stack

```sh
microbe ps                  # list services, status, IPs and ports
microbe logs [services...]  # show guest logs
microbe exec <service> [cmd...]  # run a command inside a service, no pty
microbe shell <service>     # interactive shell in a service
```

A service's guest is reachable directly on its assigned IP without SSH (`microbe exec`/`shell` go over vsock). Published ports (like `web`'s `8080:80` above) are DNAT'd via nftables and reachable from elsewhere on the LAN — but not via `localhost`/the host's own address from the same host that's running the VM (the kernel routes that through `lo`, bypassing the nftables prerouting hook, a Linux hairpin-NAT quirk rather than a microbe bug). Use the guest's direct IP to test locally.

## Tearing down

```sh
microbe down [services...]              # stop, tear down host networking
microbe down --remove-volumes           # ...and delete disk images + state
microbe rm [services...]                # remove disks/state for stopped services
microbe purge                           # sweep this stack's orphaned network devices
microbe purge all --all                 # host-wide: stop VMs, purge networks and volumes
```

Runtime data — build outputs, run dirs, logs, volumes, `state.json` — lives under `/var/lib/microbe/<stack-name>/` (the stack's `name` from `microbe.nix`), not in your project directory. It's daemon-owned, group `microbe`. `down --remove-volumes` cleans it up; nothing in your project directory needs deleting.

## Everything else

```sh
microbe config               # print the evaluated, validated config
microbe build [services...]  # render flake and build runner derivations without starting
```

See the [CLI Reference](./cli/microbe.md) for the full set of commands and flags.
