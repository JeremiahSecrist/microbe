# microbe

Docker-compose-style orchestration for [microvm.nix](https://github.com/astro/microvm.nix). Define a stack of VMs in one `microbe.nix`, then `microbe up`.

Each service in the stack becomes a real microvm.nix guest — its own kernel, its own root filesystem, real network isolation — but you configure and drive the stack the way you'd drive a docker-compose project: one declarative file, `up`/`down`/`ps`/`logs`/`exec`/`shell` for the day-to-day commands, health-gated startup order, published ports, folder-bind volumes.

Where to go next:

- [Setup](./setup.md) — install the CLI and, optionally, enable it as a NixOS module.
- [Usage](./usage.md) — write a `microbe.nix`, bring a stack up, and a walkthrough of the more interesting behavior (healthcheck-gated dependencies, published ports).
- [CLI Reference](./cli/microbe.md) — every command and flag.
- [Reference](./reference/options.md) — the `virtualisation.microbe.*`/`microbe.demoDesktop.*` NixOS module options.
- [API](./api/index.md) — generated Go package documentation.
