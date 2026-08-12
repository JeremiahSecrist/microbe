# finix-sudo.nix — fixed flake-parts module shipped with microbe.
#
# Enables sudo for finix guests. finix exports its sudo module as
# finix.nixosModules.sudo but does not include it in its default import set,
# so it must be imported explicitly. Self-registers as
# flake.nixosModules.finix-sudo (Dendritic pattern).
{
  flake.nixosModules.finix-sudo = { inputs, ... }:
    {
      imports = [ inputs.finix.nixosModules.sudo ];
      programs.sudo.enable = true;
    };
}
