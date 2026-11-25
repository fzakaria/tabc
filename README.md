# tabc

Run containers in a browser tab.

tabc is an OCI runtime, like runc. Where runc starts your container as
a Linux process, tabc starts headless Chromium and boots the container
inside a tab, on a Linux kernel compiled to WebAssembly. The tab is the
sandbox.

```console
$ docker run --rm --runtime tabc --platform linux/arm64 alpine uname -sm
Linux aarch64
```

There is no Linux underneath, so the same thing works on a Mac with no
VM and no hypervisor:

```console
$ tabc run -it alpine sh
/ # uname -sm
Linux aarch64
```

A container starts in about a second.

## Install

tabc is one file. It needs Chromium or Chrome on the machine.

With Nix, on Linux or an Apple Silicon Mac:

```console
$ nix run github:fzakaria/tabc -- run alpine echo hi
hi
```

On Linux the Nix package brings its own Chromium. On a Mac tabc looks
for Chrome in `/Applications`; set `TABC_CHROMIUM` to use another.

## Use

By itself. tabc pulls the image and runs it. The flags are the ones you
know from `docker run`: `-i`, `-t`, `-e`, `-w`, `-u`, `-m`.

```console
$ tabc run python:3.13-alpine python -c 'print(6 * 7)'
42
$ echo hello | tabc run -i alpine tr a-z A-Z
HELLO
```

With Docker. Register the runtime in `/etc/docker/daemon.json` and
restart the daemon:

```json
{ "runtimes": { "tabc": { "path": "/path/to/tabc" } } }
```

```console
$ docker run --rm -it --runtime tabc --platform linux/arm64 debian bash
```

On NixOS that is one line:

```nix
virtualisation.docker.daemon.settings.runtimes.tabc.path = "${tabc}/bin/tabc";
```

With Podman, nothing to set up:

```console
$ podman run --rm --runtime /path/to/tabc --platform linux/arm64 alpine echo hi
```

## How it works

1. The engine calls `tabc create`, as it would call runc.
2. tabc starts a supervisor, and the supervisor starts headless
   Chromium with a fresh profile on a page it serves from loopback.
3. The page boots [unkernel], a Linux kernel built for WebAssembly
   that runs aarch64 programs.
4. The guest mounts the image. Every file it opens is a request back
   to the supervisor, which reads it from the bundle, or straight out
   of the image's layers. Nothing is unpacked.
5. `tabc start` runs your program in the guest. Its input and output
   travel over a WebSocket, and so does every network connection it
   makes, which the supervisor opens from inside the container's
   network namespace.

To the engine the supervisor is the container: its pid, its exit
status, its stdio. [docs/design.md](docs/design.md) has the details.

## What to expect

- Images must have a `linux/arm64` variant. The guest is aarch64 on
  every host.
- Programs run slower than native, since they are emulated. Starting
  is fast; a long computation is not.
- No volumes, no published ports, no `docker exec`. Memory is the only
  limit that is applied.
- The kernel in the tab is young. Treat this as an experiment, not as
  somewhere to keep secrets.

## Hacking

```console
$ nix build                # the runtime, one file
$ nix flake check          # unit tests, and containers in a real Chromium
$ nix build .#tabc-darwin  # a Mac binary, built on Linux
$ nix fmt                  # before committing
```

## Credits

[unkernel] by Pooya Parsa does the hard part: Linux as a WebAssembly
module, and the SDK that boots it.

## License

MIT, please see [LICENSE](LICENSE). unkernel's kernel is GPL-2.0.

[unkernel]: https://github.com/pithings/unkernel
