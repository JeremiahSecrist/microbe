# finix-configurations.nix — fixed flake-parts module shipped with microbe.
#
# flake.nixosConfigurations is declared by flake-parts itself as a
# lazyAttrsOf (see flake-parts' own modules/nixosConfigurations.nix), so
# each per-service part file assigning flake.nixosConfigurations.<name>
# merges fine with every other one. flake.finixConfigurations has no such
# declaration anywhere -- not in flake-parts (which has never heard of
# finix), not previously in microbe's own fixed modules -- so with two or
# more finix services each contributing flake.finixConfigurations.<name>
# in their own file, flake-parts' module system has no merge strategy and
# fails outright ("The option `flake.finixConfigurations' is defined
# multiple times", verified live: a two-finix-service stack failed `nix
# eval` on the second service until this module existed). Mirrors
# flake-parts' own nixosConfigurations declaration exactly (same
# lazyAttrsOf raw type) so any number of finix services can coexist.
{ lib, ... }:
{
  options.flake.finixConfigurations = lib.mkOption {
    type = lib.types.lazyAttrsOf lib.types.raw;
    default = { };
    description = "Instantiated finix configurations, one per finix service.";
  };
}
