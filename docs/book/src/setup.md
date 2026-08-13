# Setup

Requires [Nix](https://nixos.org/download) with flakes enabled.

## As a standalone binary

```sh
nix build github:JeremiahSecrist/microbe#microbe
./result/bin/microbe --version
```

## As a NixOS module

Add it to a NixOS host module:

```nix
{
  inputs.microbe.url = "github:JeremiahSecrist/microbe";

  # in your host config:
  virtualisation.microbe.enable = true;
  virtualisation.microbe.package = inputs.microbe.packages.${system}.microbe;
}
```

The module creates the `microbe` group and root `microbe-provisiond` daemon that host networking (bridges, taps, published ports) is provisioned through — members of that group can run `microbe up`/`down`/etc. without `sudo`. See [Reference](./reference/options.md) for the full set of module options, including which users get added to the group.

## Man pages

Man pages for every command install alongside the binary (`share/man/man1`). Once installed, `man microbe` and `man microbe-up` (etc.) work directly.
