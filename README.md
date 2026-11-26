# tabc

Run containers in a browser tab.

tabc is an OCI runtime, like runc. Where runc starts your container as
a Linux process, tabc starts headless Chromium and boots the container
inside a tab, on a Linux kernel compiled to WebAssembly. The tab is the
sandbox.

<p align="center"><img src="docs/img/demo.webp" width="720" alt="docker run with the tabc runtime on Linux, then tabc run on a Mac with no VM running"></p>

```console
$ docker run --rm --runtime tabc --platform linux/arm64 alpine uname -sm
Linux aarch64
```

There is no Linux underneath, so it works on a Mac with no VM and no
hypervisor. The tab can be one you watch, in your own browser, and
type into from either side:

```console
$ tabc run -it --open -p 8123:80 nginx:alpine sh
```

<p align="center"><img src="docs/img/tab.webp" width="640" alt="A terminal above a Chromium window. A tab opens, boots Linux and shows a shell; commands typed in the tab and in the terminal appear in both. nginx starts in the container and a second tab loads its page from localhost:8123."></p>

Compose works too, with every service in a tab of its own:

```console
$ tocker compose up
```

<p align="center"><img src="docs/img/compose.webp" width="640" alt="A terminal runs tocker compose up; a Chromium window under it shows a pane per service, Redis starting in one and a web server in the other. A second tab loads the web service and its count goes up."></p>

A container starts in under a second: 0.87 s on a Ryzen laptop, 0.44 s
on an M4.

## Install

With Nix, on Linux or an Apple Silicon Mac:

```console
$ nix run github:fzakaria/tabc -- run alpine echo hi
hi
```

tabc is one file, and `tocker` is the same file under another name. On
Linux the Nix package brings its own Chromium. On a Mac tabc looks for
Chrome in `/Applications`, or set `TABC_CHROMIUM`. With `--open` the
container runs in the browser you already have. The browser needs
WebAssembly JSPI: a recent Chrome or Chromium has it, and so does
Firefox 160. In a browser without it, tabc prints what is missing and
exits.

## Use

### On its own

tabc pulls the image and runs it. The flags are the ones you know from
`docker run`.

```console
$ tabc run python:3.13-alpine python -c 'print(6 * 7)'
42
$ echo hello | tabc run -i alpine tr a-z A-Z
HELLO
$ tabc run -p 8080:80 nginx:alpine          # then curl localhost:8080
$ tabc run -v "$PWD":/work -w /work alpine ls
```

| flag                   | what it does                                    |
| ---------------------- | ----------------------------------------------- |
| `-i`, `-t`             | keep stdin open, give the program a terminal    |
| `-e`, `-w`, `-u`, `-m` | environment, directory, user, memory            |
| `-p host:container`    | publish a port, on loopback unless you say      |
| `-v dir:/path[:ro]`    | mount a directory, live and writable            |
| `--name`, `--network`  | let containers find each other by name          |
| `--open`, `--link`     | run in your own browser instead of a hidden one |

### In your browser

```console
$ tabc run -it --open alpine sh
```

The container opens as a tab you can see, with a terminal in it. Type
in the tab or in the terminal you started it from; each shows the
other. `--link` prints the address and waits for you to open it.

### Compose, without Docker

The `docker` program is only a client: every command is a request to a
daemon that remembers which containers exist. On a Mac there is no
such daemon, so tabc has one. `tocker` starts it if it isn't running
and then becomes the real `docker`, so every command you know works,
Compose included.

```console
$ nix run github:fzakaria/tabc#tocker -- run -it alpine sh
$ tocker compose up
$ tocker --dashboard compose up   # every service as a pane in one page
$ tocker --stop                   # stop the daemon and its containers
```

Nothing in your `compose.yaml` changes. [examples/compose](examples/compose)
is a web server and the Redis it counts its visits in.

### With Docker, as its runtime

If you do have Docker, tabc can be the runtime it uses instead of runc.
Register it in `/etc/docker/daemon.json` and restart the daemon:

```json
{ "runtimes": { "tabc": { "path": "/path/to/tabc" } } }
```

On NixOS that is one line:

```nix
virtualisation.docker.daemon.settings.runtimes.tabc.path = "${tabc}/bin/tabc";
```

Then name it per container:

```console
$ docker run --rm -it --runtime tabc --platform linux/arm64 debian bash
```

For Compose the runtime is a line on each service, not a flag to
`docker compose up`:

```yaml
services:
  web:
    image: python:3.13-alpine
    runtime: tabc
    platform: linux/arm64
```

Docker's networks, `-p` and volumes then work as they do with runc.
Podman needs no setup:

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
6. A connection the other way arrives at the supervisor, which listens
   on the ports the program opens and carries each one into the tab.

To the engine the supervisor is the container: its pid, its exit
status, its stdio. [docs/design.md](docs/design.md) has the details.

## What to expect

- Images must have a `linux/arm64` variant. The guest is aarch64 on
  every host.
- Programs run 11 to 36 times slower than under runc, since they are
  emulated. Starting is fast; a long computation is not.
- Each container is a whole browser: about 540 MiB of memory when idle.
- No `docker exec`, no UDP, no image builds on a Mac. Memory is the
  only limit that is applied.
- A container you open in your own browser is behind a thinner wall
  than one in the hidden browser tabc starts.
- The kernel in the tab is young. Treat this as an experiment, not as
  somewhere to keep secrets.

## Hacking

```console
$ nix build                # the runtime, one file
$ nix flake check          # unit tests, and containers in a real Chromium
$ nix build .#tabc-darwin  # a Mac binary, built on Linux
$ nix fmt                  # before committing
```

`tools/bench/` has the scripts behind the numbers on this page, and
`tools/demo/` the ones that recorded its two animations.

## Credits

[unkernel] by Pooya Parsa does the hard part: Linux as a WebAssembly
module, and the SDK that boots it.

## License

MIT, please see [LICENSE](LICENSE). unkernel's kernel is GPL-2.0.

[unkernel]: https://github.com/pithings/unkernel
