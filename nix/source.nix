# The Go sources, and nothing else: a change to the page, the docs or
# the flake rebuilds neither the runtime nor the guest's init.
{ lib }:
lib.fileset.toSource {
  root = ../.;
  fileset = lib.fileset.unions [
    ../go.mod
    ../go.sum
    ../cmd
    ../internal
  ];
}
