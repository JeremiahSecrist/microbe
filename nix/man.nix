# Section-1 man pages for the microbe CLI, generated from the same command
# tree docs.nix's CLI reference page uses (see internal/gendocs). Kept as
# its own flake output rather than folded into the microbe package's
# postInstall: microbe is deliberately minimal (no subPackages, filtered
# source, doCheck = false) since it's what NixOS hosts install via
# virtualisation.microbe.package, and docs already sets the precedent of a
# sibling output sharing goSrc/vendorHash instead of riding on the main
# derivation.
{ pkgs, goSrc, vendorHash }:
let
  gendocsPkg = import ./gendocs.nix { inherit pkgs goSrc vendorHash; };
in
pkgs.stdenvNoCC.mkDerivation {
  pname = "microbe-man";
  version = "0.1.0";
  dontUnpack = true;

  installPhase = ''
    runHook preInstall
    mkdir -p $out/share/man/man1
    ${gendocsPkg}/bin/gendocs man $out/share/man/man1
    runHook postInstall
  '';
}
