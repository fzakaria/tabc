# The runtime (cmd/tabc), built knowing where its share directory and
# its browser are, so an engine can be pointed at the binary and nothing
# else. TABC_SHARE and TABC_CHROMIUM override both.
{
  pkgs,
  share ? import ./share.nix { inherit pkgs; },
  chromium ? pkgs.chromium,
}:
let
  go = import ./go.nix;
in
pkgs.buildGoModule {
  pname = "tabc";
  inherit (go) version vendorHash;
  src = import ./source.nix { inherit (pkgs) lib; };
  subPackages = [ "cmd/tabc" ];

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${go.version}"
    "-X main.shareDirBuilt=${share}"
    "-X main.chromiumBuilt=${chromium}/bin/chromium"
  ];

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
    license = pkgs.lib.licenses.mit;
    mainProgram = "tabc";
    platforms = pkgs.lib.platforms.linux;
  };
}
