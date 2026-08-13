# Builds cmd/gendocs, the CLI markdown/man-page generator (see
# internal/gendocs). Shared by nix/docs.nix (markdown, for the mdBook) and
# nix/man.nix (man pages) so the buildGoModule block isn't duplicated.
{ pkgs, goSrc, vendorHash }:
pkgs.buildGoModule {
  pname = "microbe-gendocs";
  version = "0.1.0";
  src = goSrc;
  inherit vendorHash;
  subPackages = [ "./cmd/gendocs" ];
  doCheck = false;
}
