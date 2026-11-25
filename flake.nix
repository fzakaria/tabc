{
  description = "An OCI runtime that runs each container in a browser tab";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system);

      # The guest is aarch64 on every host, so what runs inside a
      # container is always taken from here.
      guestPkgs = nixpkgs.legacyPackages.aarch64-linux;
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        rec {
          # the runtime: point a container engine at bin/tabc
          tabc = import ./nix/package.nix { inherit pkgs share; };
          default = tabc;

          # what the supervisor serves a container's page: the page, the
          # unkernel SDK with its kernel, and the guest's init. The
          # package embeds these; for a `go build` of the runtime,
          # TABC_SHARE names this.
          share = import ./nix/share.nix { inherit pkgs; };

          # the runtime for a Mac, built here: one file to copy over.
          # Go cross-compiles with no toolchain but its own.
          tabc-darwin = import ./nix/package.nix {
            inherit pkgs share;
            target = {
              os = "darwin";
              arch = "arm64";
            };
          };

          # a bundle to run by hand, and the one the end-to-end check
          # runs: `nix run . -- run --bundle "$(nix build .#example --print-out-paths)" demo`
          example = import ./nix/example.nix { inherit pkgs guestPkgs; };
        }
      );

      checks = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          packages = self.packages.${system};
        in
        {
          # the Go tests run as the package builds
          inherit (packages) tabc;
        }
        # the runtime end to end, in a real headless Chromium, where
        # nixpkgs has one to run
        // nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          e2e = import ./nix/e2e.nix {
            inherit pkgs;
            inherit (packages) tabc;
            bundle = packages.example;
          };
        }
      );

      formatter = forAllSystems (
        system:
        import ./nix/formatter.nix {
          pkgs = nixpkgs.legacyPackages.${system};
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          # `go build ./cmd/tabc` here finds the page and the browser
          # through the two variables.
          default =
            pkgs.mkShell {
              packages = [
                pkgs.go
                pkgs.gopls
                pkgs.jq
              ];
              TABC_SHARE = self.packages.${system}.share;
            }
            // nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
              TABC_CHROMIUM = "${pkgs.chromium}/bin/chromium";
            };
        }
      );
    };
}
