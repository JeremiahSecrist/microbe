# Builds docs/book/ into a static site, overlaying two generated sections:
# Go package docs (gomarkdoc) and NixOS module option docs (nixosOptionsDoc,
# the same mechanism the NixOS manual itself uses).
{ pkgs, lib, nixpkgs, system, goSrc, vendorHash }:

let
  # Auto-discover modules/*.nix so a new module file is picked up without
  # editing this build -- mirrors how flake.nix imports them into hosts.
  moduleFiles = builtins.filter (lib.hasSuffix ".nix")
    (builtins.attrNames (builtins.readDir ../modules));
  moduleImports = map (name: ../modules + "/${name}") moduleFiles;

  # Full NixOS option tree with our modules mixed in. nixosSystem always
  # includes the base NixOS module list, so every option our modules
  # reference (environment.etc, services.xserver.enable, boot.kernelModules,
  # ...) is already declared -- no need to hand-assemble a minimal module
  # set just to make evalModules happy.
  optionsEval = nixpkgs.lib.nixosSystem {
    inherit system;
    modules = moduleImports;
  };

  # Scoped to just the namespaces our modules currently add (otherwise this
  # would render every NixOS option that exists). A future module
  # introducing a genuinely new top-level namespace -- not
  # virtualisation.microbe or microbe.* -- needs a one-line addition here.
  optionsDoc = pkgs.nixosOptionsDoc {
    options = {
      virtualisation.microbe = optionsEval.options.virtualisation.microbe;
      microbe = optionsEval.options.microbe;
    };
  };

  # Doc comments for every internal/* package, dev-reference style. Reuses
  # the same filtered source + vendorHash as the microbe CLI package itself
  # (see flake.nix) so this doesn't re-fetch/re-pin Go module deps.
  goDocs = pkgs.buildGoModule {
    pname = "microbe-godocs";
    version = "0.1.0";
    src = goSrc;
    inherit vendorHash;
    nativeBuildInputs = [ pkgs.gomarkdoc ];
    dontBuild = true;
    doCheck = false;
    installPhase = ''
      export HOME=$TMPDIR
      gomarkdoc ./... > $out
    '';
  };
in
pkgs.stdenvNoCC.mkDerivation {
  pname = "microbe-docs";
  version = "0.1.0";
  src = ../docs/book;
  nativeBuildInputs = [ pkgs.mdbook ];

  buildPhase = ''
    runHook preBuild
    cp ${optionsDoc.optionsCommonMark} src/reference/options.md
    cp ${goDocs} src/api/index.md
    mdbook build --dest-dir "$PWD/out"
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall
    cp -r out $out
    runHook postInstall
  '';
}
