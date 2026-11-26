# Everything the supervisor serves to a container's page: the page
# itself, the unkernel SDK with its kernel, the terminal the page shows
# the program on, and the guest's init.
{ pkgs }:
let
  unkernel = import ./unkernel.nix { inherit pkgs; };
  init = import ./init.nix { inherit pkgs; };
  xterm = import ./xterm.nix { inherit pkgs; };
in
pkgs.runCommand "tabc-share" { passthru = { inherit unkernel init; }; } ''
  mkdir -p $out
  cp -r ${../page} $out/page
  ln -s ${unkernel} $out/unkernel
  ln -s ${xterm} $out/xterm
  ln -s ${init} $out/init
''
