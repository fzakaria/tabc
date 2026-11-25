# Everything the supervisor serves to a container's page: the page
# itself, the unkernel SDK with its kernel, and the guest's init.
{ pkgs }:
let
  unkernel = import ./unkernel.nix { inherit pkgs; };
  init = import ./init.nix { inherit pkgs; };
in
pkgs.runCommand "tabc-share" { passthru = { inherit unkernel init; }; } ''
  mkdir -p $out
  cp -r ${../page} $out/page
  ln -s ${unkernel} $out/unkernel
  ln -s ${init} $out/init
''
