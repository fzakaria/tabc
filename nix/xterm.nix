# xterm.js: the terminal a container's page shows its program on, so a
# container opened in a browser can be read, and typed into, like one
# opened in a terminal. The fit addon sizes a terminal to its pane.
#
# Both packages ship one script that defines a global, loaded by the
# page before its own module.
{ pkgs }:
let
  npm =
    name: file: version: sha256:
    pkgs.fetchurl {
      url = "https://registry.npmjs.org/${name}/-/${file}-${version}.tgz";
      inherit sha256;
    };

  xterm = npm "@xterm/xterm" "xterm" "5.5.0" "1ljr3x4jcgbqq8j8sklal74q6g6vi0zfimy5ihc708c746klz5dx";
  fit =
    npm "@xterm/addon-fit" "addon-fit" "0.10.0"
      "0cqbp8mi0w86wv1908ng9379hqy7c01fbhgdabnmwga5f94w8yli";
in
pkgs.runCommand "tabc-xterm" { } ''
  mkdir -p unpack $out

  tar -xzf ${xterm} -C unpack
  cp unpack/package/lib/xterm.js unpack/package/css/xterm.css $out/
  cp unpack/package/LICENSE $out/LICENSE
  rm -r unpack/package

  tar -xzf ${fit} -C unpack
  cp unpack/package/lib/addon-fit.js $out/
''
