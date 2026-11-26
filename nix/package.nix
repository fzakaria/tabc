# The runtime (cmd/tabc) as one file: the page, the kernel and the
# guest's init are embedded in the binary, and on Linux the binary knows
# which browser to run, so an engine can be pointed at bin/tabc and
# nothing else. TABC_SHARE and TABC_CHROMIUM override both.
#
# `target` builds for another system than the one building, which Go
# does with no toolchain but its own: `{ os = "darwin"; arch = "arm64"; }`
# is a binary for a Mac, built on Linux.
{
  pkgs,
  share ? import ./share.nix { inherit pkgs; },
  chromium ? if pkgs.stdenv.hostPlatform.isLinux then pkgs.chromium else null,
  # The docker command line tocker becomes, Compose included.
  docker ? pkgs.docker-client,
  target ? null,
}:
let
  go = import ./go.nix;
  lib = pkgs.lib;

  # A binary for another system has no browser to be told about: the
  # browser is found where it runs.
  pinned = if target == null then chromium else null;

  native = pkgs.buildGoModule {
    pname = "tabc";
    inherit (go) version vendorHash;
    src = import ./source.nix { inherit lib; };
    subPackages = [ "cmd/tabc" ];

    # What the binary embeds, copied in as files: the share's own tree
    # is links into the store, which cannot be embedded.
    preBuild = ''
      cp -rL ${share} internal/assets/share
      chmod -R u+w internal/assets/share
    '';
    tags = [ "embedshare" ];

    ldflags = [
      "-s"
      "-w"
      "-X main.version=${go.version}"
    ]
    ++ lib.optional (pinned != null) "-X main.chromiumBuilt=${pinned}/bin/chromium"
    ++ lib.optional (target == null) "-X main.dockerBuilt=${docker}/bin/docker";

    # tocker is tabc under another name: the docker command line,
    # pointed at tabc's own daemon.
    postInstall = ''
      ln -s tabc $out/bin/tocker
    '';

    # Every package's tests, not only the command's.
    checkPhase = ''
      runHook preCheck
      go test ./...
      runHook postCheck
    '';

    passthru = { inherit share chromium; };

    meta = {
      description = "An OCI runtime that runs each container in a browser tab";
      homepage = "https://github.com/fzakaria/tabc";
      license = lib.licenses.mit;
      mainProgram = "tabc";
      platforms = lib.platforms.linux ++ lib.platforms.darwin;
    };
  };

  # buildGoModule takes the target from the Go toolchain it was given,
  # over anything its caller's env says, so another target is set on the
  # finished derivation. A binary for another system cannot run its
  # tests here.
  cross = native.overrideAttrs (old: {
    pname = "tabc-${target.os}-${target.arch}";
    env = old.env // {
      GOOS = target.os;
      GOARCH = target.arch;
      CGO_ENABLED = "0";
    };
    doCheck = false;
  });
in
if target == null then
  native
else
  # Go installs a cross-compiled binary under bin/<os>_<arch>/.
  pkgs.runCommand "tabc-${target.os}-${target.arch}-${go.version}" { } ''
    install -Dm755 "$(find ${cross}/bin -type f -name tabc)" $out/bin/tabc
    ln -s tabc $out/bin/tocker
  ''
