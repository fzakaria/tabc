# What a release carries for someone without Nix: tabc for each system,
# as one archive per system, and the checksums of the archives.
#
# Each archive holds tabc, tocker (tabc under its other name), and the
# licenses of tabc and of what the binary embeds: the unkernel SDK with
# its kernel, and the terminal the page shows. The binary is built for
# the system and knows no paths of this one's, so the browser and the
# docker command line are found where the binary runs.
{
  pkgs,
  share ? import ./share.nix { inherit pkgs; },
}:
let
  go = import ./go.nix;
  lib = pkgs.lib;

  # The systems a release is built for, as Go names them.
  targets = [
    {
      os = "linux";
      arch = "amd64";
    }
    {
      os = "linux";
      arch = "arm64";
    }
    {
      os = "darwin";
      arch = "arm64";
    }
    {
      os = "darwin";
      arch = "amd64";
    }
  ];

  # One system's archive: a directory named for the system, so that
  # unpacking two systems' archives side by side keeps them apart. The
  # name has no version in it, so the newest release's archive has an
  # address that never changes (releases/latest/download/<name>).
  archive =
    target:
    let
      built = import ./package.nix { inherit pkgs share target; };
      name = "tabc-${target.os}-${target.arch}";
    in
    ''
      mkdir -p ${name}/licenses/unkernel ${name}/licenses/xterm
      cp -P ${built}/bin/tabc ${built}/bin/tocker ${name}/
      cp ${../LICENSE} ${name}/LICENSE
      cp ${share}/unkernel/LICENSE ${share}/unkernel/LICENSE-MIT \
        ${share}/unkernel/THIRD-PARTY.md ${name}/licenses/unkernel/
      cp ${share}/xterm/LICENSE ${name}/licenses/xterm/

      # The same bytes whenever the same source is built: names in
      # order, no owner and one date. Files in the store are read-only,
      # which someone unpacking a release would have to undo.
      tar --sort=name --owner=0 --group=0 --numeric-owner --mtime=@1 --mode=u+w \
        -cf - ${name} | gzip -n > $out/${name}.tar.gz
    '';
in
pkgs.runCommand "tabc-release-${go.version}" { passthru = { inherit (go) version; }; } ''
  mkdir -p $out
  ${lib.concatMapStrings archive targets}
  cd $out
  sha256sum *.tar.gz > SHA256SUMS
''
