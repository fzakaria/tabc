# unkernel: a Linux kernel compiled to WebAssembly, with the JavaScript
# SDK that boots it (https://github.com/pithings/unkernel). The npm
# package carries the kernel prebuilt, base system included, so there is
# nothing to build.
#
# The page loads four files of it: the browser entry, the chunk the
# entry imports, and the kernel twice over, built for one CPU and for
# several. The Node entry, the CLI and the GPU renderers stay behind: a
# container's page draws nothing.
{ pkgs }:
let
  version = "0.2.0-261125-cec0a49";

  tarball = pkgs.fetchurl {
    url = "https://registry.npmjs.org/unkernel/-/unkernel-${version}.tgz";
    sha256 = "1i85h53db9fzj87ilsiyg4dv0qih01ali3ki2gdv93f4x9s5fzib";
  };

  # The package carries the one-CPU kernel, and the several-CPU kernel
  # as a binary difference from the one-CPU kernel. The SDK applies the
  # difference in the page at every boot, which takes about 150 ms.
  # This script applies the difference once, here, with the SDK's own
  # function.
  deriveSmp = pkgs.writeText "derive-smp.mjs" ''
    import { readFile, writeFile } from "node:fs/promises";

    const [sdk, bundle, out] = process.argv.slice(2);
    const { smpKernelFromBundle } = await import(sdk);

    const smp = await smpKernelFromBundle(new Uint8Array(await readFile(bundle)));
    if (smp === undefined) {
      throw new Error("the kernel bundle carries no several-CPU kernel");
    }
    await writeFile(out, smp);
  '';
in
pkgs.runCommand "tabc-unkernel-${version}"
  {
    nativeBuildInputs = [ pkgs.nodejs ];
    passthru = { inherit version; };
  }
  ''
    mkdir -p unpack $out/_chunks
    tar -xzf ${tarball} -C unpack

    cp unpack/package/dist/web.mjs $out/web.mjs
    cp unpack/package/dist/_chunks/core.mjs $out/_chunks/core.mjs
    cp unpack/package/dist/unkernel.wasm $out/unkernel.wasm

    node ${deriveSmp} "$out/_chunks/core.mjs" "$out/unkernel.wasm" "$out/unkernel-smp.wasm"

    # The kernel is GPL-2.0 and the SDK MIT; both licenses and the notices
    # of what the package carries travel with the files.
    cp unpack/package/LICENSE unpack/package/LICENSE-MIT unpack/package/THIRD-PARTY.md $out/
    echo ${version} > $out/VERSION
  ''
