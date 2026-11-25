# A bundle to run: busybox for aarch64 from nixpkgs, with its closure,
# as a container's root file system, and a config.json that runs a
# shell. What a container engine would unpack from an image and hand
# the runtime, made without an engine.
#
# The guest is aarch64 on every host, so the packages are aarch64's
# whatever builds this: on an x86-64 machine they come from the binary
# cache, since nothing here can build them.
{ pkgs, guestPkgs }:
let
  program = guestPkgs.busybox;
  closure = pkgs.closureInfo { rootPaths = [ program ]; };

  config = {
    ociVersion = "1.0.2";
    hostname = "tabc";
    root.path = "rootfs";
    process = {
      terminal = false;
      user = {
        uid = 0;
        gid = 0;
      };
      args = [ "sh" ];
      env = [ "PATH=/bin" ];
      cwd = "/";
    };
  };
in
pkgs.runCommand "tabc-example-bundle" { } ''
  mkdir -p $out/rootfs/nix/store $out/rootfs/etc $out/rootfs/tmp

  # The closure, where its binaries expect to find each other.
  while read -r path; do
    cp -a "$path" $out/rootfs/nix/store/
  done < ${closure}/store-paths

  ln -s ${program}/bin $out/rootfs/bin
  echo 'root:x:0:0:root:/root:/bin/sh' > $out/rootfs/etc/passwd

  cp ${pkgs.writeText "config.json" (builtins.toJSON config)} $out/config.json
''
