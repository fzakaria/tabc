# The runtime end to end: a real headless Chromium boots the guest and
# runs programs from the example bundle. Each case replaces the
# bundle's program and checks what came back: the output, the exit
# status, the input.
{
  pkgs,
  tabc,
  bundle,
}:
pkgs.runCommand "tabc-e2e"
  {
    nativeBuildInputs = [
      tabc
      pkgs.jq
    ];
  }
  ''
    # A bundle of its own per case: the same root file system, and a
    # config.json with the case's program.
    run() {
      local name=$1
      shift
      mkdir -p bundles/$name
      ln -s ${bundle}/rootfs bundles/$name/rootfs
      # After "--" every word is the program's, "-c" included.
      jq '.process.args = $ARGS.positional' --args -- "$@" \
        < ${bundle}/config.json > bundles/$name/config.json
      tabc --root "$PWD/state" run --bundle bundles/$name $name
    }

    echo "output"
    test "$(run output echo hi < /dev/null)" = hi

    echo "exit status"
    status=0
    run status sh -c 'exit 7' < /dev/null || status=$?
    test "$status" = 7

    echo "stderr apart from stdout"
    run streams sh -c 'echo out; echo err >&2' < /dev/null > out.txt 2> err.txt
    test "$(cat out.txt)" = out
    test "$(cat err.txt)" = err

    echo "input, to its end"
    test "$(echo from the host | run input cat)" = "from the host"

    echo "a writable root over a read-only image"
    test "$(run writable sh -c 'echo x > /tmp/f && cat /tmp/f' < /dev/null)" = x

    echo "the guest is its own machine"
    test "$(run machine uname -m < /dev/null)" = aarch64

    touch $out
  ''
