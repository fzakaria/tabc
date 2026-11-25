# The guest's init (cmd/tabc-init): the first program the guest runs
# for a container. The guest is aarch64 whatever the host is, and the
# guest's base system is not the container's, so init is a static
# aarch64 binary. Go cross-compiles one with no C toolchain.
{ pkgs }:
let
  go = import ./go.nix;

  # buildGoModule takes the target from the Go toolchain it was given,
  # over anything its caller's env says, so the architecture is set on
  # the finished derivation.
  native = pkgs.buildGoModule {
    pname = "tabc-init";
    inherit (go) version vendorHash;
    src = import ./source.nix { inherit (pkgs) lib; };
    subPackages = [ "cmd/tabc-init" ];

    env.CGO_ENABLED = "0";
    ldflags = [
      "-s"
      "-w"
    ];

    # An aarch64 test binary does not run on every build host.
    doCheck = false;
  };

  cross = native.overrideAttrs (old: {
    env = old.env // {
      GOOS = "linux";
      GOARCH = "arm64";
    };
  });
in
# Go installs a cross-compiled binary under bin/<os>_<arch>/ and a
# native one under bin/, so the binary is picked up from either.
pkgs.runCommand "tabc-init-${go.version}" { } ''
  install -Dm755 "$(find ${cross}/bin -type f -name tabc-init)" $out
''
