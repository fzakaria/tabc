// One container in one tab.
//
// The supervisor (internal/supervisor) started this browser and serves
// this page. The page boots a Linux guest with unkernel, gives the
// guest the container image as a file system whose every lookup is a
// request back to the supervisor, and runs the container's program
// when the supervisor says to. The program's output goes to the
// supervisor over one WebSocket, and its input, signals and window
// size come back the same way.

import { createKernel, FsError, MemoryFs } from "./unkernel/web.mjs";

// What the supervisor serves next to this page.
const CONFIG_URL = "config.json";
const SPEC_URL = "spec.json";
const INIT_URL = "init";
const FS_URL = "fs/";
const CONTROL_URL = "ctl";
const NET_DNS_URL = "net/dns";
const NET_TCP_URL = "net/tcp";
const INBOUND_URL = "in";

// Where the guest finds the image, and init with the container's spec.
const LOWER_DIR = "/tabc/lower";
const BIN_DIR = "/tabc/bin";
const INIT_NAME = "init";
// The argument that makes init watch the program's open ports.
const PORTS_FLAG = "--ports";
const SPEC_NAME = "spec.json";
const EXECUTABLE = 0o755;

// The image's root, as the guest names it.
const ROOT = "/";

// The image's lookups wait on the supervisor's disk, never on a network,
// so the SDK's request timeout is off: a slow disk is not an error.
const NO_TIMEOUT = 0;

// The first byte of a binary message says which stream its bytes are.
const Stream = Object.freeze({
  STDIN: 0,
  STDOUT: 1,
  STDERR: 2,
});

// The messages in each direction, by their `t` field.
const FromSupervisor = Object.freeze({
  START: "start",
  EOF: "eof",
  KILL: "kill",
  RESIZE: "resize",
  CONNECT: "connect",
});
const ToSupervisor = Object.freeze({
  READY: "ready",
  EXIT: "exit",
  LOG: "log",
  ERROR: "error",
  LISTEN: "listen",
  UNLISTEN: "unlisten",
});

// How a program's end is reported by the SDK, and the status a program
// ended by a signal is given, as a shell gives one.
const END_EXIT = "exit";
const END_SIGNAL = "signal";
const SIGNAL_STATUS_BASE = 128;
// The status for a session that ended with neither, a lost guest.
const LOST_STATUS = 255;

// What a WebSocket holds unsent before its sender waits, and how often
// a waiting sender looks again.
const SEND_HIGH_WATER = 1 << 20;
const SEND_POLL_MS = 5;

// How near the end of the output a reader has to be for new output to
// scroll into view.
const FOLLOW_SLACK_PX = 40;

// What the page shows of the container: its name, how it is doing, and
// what the program printed. The states are the status dot's colours
// (index.html).
const titleElement = document.getElementById("title");
const statusElement = document.getElementById("status");
const outputElement = document.getElementById("output");
const State = Object.freeze({
  WAITING: "waiting",
  RUNNING: "running",
  EXITED: "exited",
  FAILED: "failed",
});

// The most characters of output the page keeps on show. A container
// may print for days; the page is a window on the end of it.
const OUTPUT_LIMIT = 200000;

// Escape sequences a program prints for a terminal, which a page that
// only shows text leaves out: colours, cursor movement, window titles.
const ESCAPES = /\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-_]/g;

function setStatus(state, text) {
  statusElement.dataset.state = state;
  statusElement.textContent = text;
}

// One decoder per stream, since a character may arrive split across two
// chunks.
const decoders = new Map();

// Add a chunk of the program's output to the page.
function show(stream, chunk) {
  if (!decoders.has(stream)) {
    decoders.set(stream, new TextDecoder());
  }
  const text = decoders.get(stream).decode(chunk, { stream: true }).replace(ESCAPES, "");
  if (text === "") {
    return;
  }

  // Follow the output only if the reader is already at its end.
  const atEnd =
    outputElement.scrollHeight - outputElement.scrollTop - outputElement.clientHeight <
    FOLLOW_SLACK_PX;
  const span = document.createElement("span");
  if (stream === Stream.STDERR) {
    span.className = "stderr";
  }
  span.textContent = text;
  outputElement.append(span);

  while (outputElement.textContent.length > OUTPUT_LIMIT && outputElement.firstChild !== span) {
    outputElement.firstChild.remove();
  }
  if (atEnd) {
    outputElement.scrollTop = outputElement.scrollHeight;
  }
}

const control = new WebSocket(new URL(CONTROL_URL, location.href).href.replace(/^http/, "ws"));
control.binaryType = "arraybuffer";

// Tell the supervisor something as JSON.
function tell(type, fields = {}) {
  control.send(JSON.stringify({ t: type, ...fields }));
}

// Anything that goes wrong here ends the container: the supervisor
// reports the message and exits.
function fatal(error) {
  const message = error?.stack ?? String(error);
  setStatus(State.FAILED, "failed");
  show(Stream.STDERR, new TextEncoder().encode(`\n${message}\n`));
  if (control.readyState === WebSocket.OPEN) {
    tell(ToSupervisor.ERROR, { message });
  }
}
addEventListener("error", (event) => fatal(event.error ?? event.message));
addEventListener("unhandledrejection", (event) => fatal(event.reason));

// One JSON document from the supervisor.
async function fetchJson(url) {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`${url}: ${response.status}`);
  }
  return response.json();
}

// The container image as the guest's file system. Read-only: the
// guest's init puts a writable layer over it.
//
// An image does not change under a running container, so nothing is
// asked twice. A directory's listing carries each entry's attributes
// and each link's target, and every lookup is answered from the
// listing of the directory the path is in: one request per directory
// the guest ever looks into, however many names it tries there. A
// program that searches ten directories for a library asks ten
// questions, and the nine answers of "not here" cost nothing more.
function imageFs() {
  const call = async (op, path, extra = "") => {
    const response = await fetch(`${FS_URL}${op}?p=${encodeURIComponent(path)}${extra}`);
    if (response.ok) {
      return response;
    }

    // The supervisor names the errno the guest should see.
    const { code } = await response.json();
    throw new FsError(code);
  };

  // Each directory's entries by name, as a promise so that lookups
  // arriving together share one request. A failed listing is forgotten,
  // so the error is the guest's and not the cache's.
  const listings = new Map();
  const listing = (path) => {
    let entries = listings.get(path);
    if (entries === undefined) {
      entries = call("dir", path)
        .then((response) => response.json())
        .then((list) => new Map(list.map((entry) => [entry.name, entry])));
      entries.catch(() => listings.delete(path));
      listings.set(path, entries);
    }
    return entries;
  };

  // The root has no directory to be listed in, so its attributes are
  // asked for once by name.
  let rootAttr;
  const entryOf = async (path) => {
    if (path === ROOT) {
      rootAttr ??= call("attr", ROOT).then((response) => response.json());
      return { attr: await rootAttr };
    }

    const cut = path.lastIndexOf("/");
    const entries = await listing(cut === 0 ? ROOT : path.slice(0, cut));
    const entry = entries.get(path.slice(cut + 1));
    if (entry === undefined) {
      throw new FsError("ENOENT");
    }
    return entry;
  };

  return {
    getattr: async (path) => (await entryOf(path)).attr,
    readdir: async (path) =>
      [...(await listing(path)).values()].map(({ name, type, ino }) => ({ name, type, ino })),
    readlink: async (path) => (await entryOf(path)).target,
    open: async (path, flags) => {
      if (flags.write) {
        throw new FsError("EROFS");
      }

      // Opening checks the file is there; each read names its range.
      const { attr } = await entryOf(path);
      return {
        read: async (offset, length) =>
          new Uint8Array(
            await (await call("read", path, `&o=${offset}&n=${length}`)).arrayBuffer(),
          ),
        getattr: () => attr,
      };
    },
  };
}

// The guest's network: each TCP connection the guest opens is one
// WebSocket to the supervisor, which dials the host and port from the
// container's network namespace, and each name is looked up there too.
function supervisorNet() {
  const tcpUrl = new URL(NET_TCP_URL, location.href).href.replace(/^http/, "ws");

  return {
    async lookup(name) {
      const response = await fetch(`${NET_DNS_URL}?name=${encodeURIComponent(name)}`);
      if (!response.ok) {
        throw Object.assign(new Error(`no such host: ${name}`), { code: "ENOTFOUND" });
      }
      return response.json();
    },

    connect({ host, port }, events) {
      const socket = new WebSocket(`${tcpUrl}?host=${encodeURIComponent(host)}&port=${port}`);
      socket.binaryType = "arraybuffer";

      // Data that arrived while the guest's stack asked for a pause.
      let paused = false;
      let held = [];
      let closed = false;
      let draining = false;

      const close = (error) => {
        if (closed) {
          return;
        }
        closed = true;
        events.onClose(error);
      };

      // The supervisor's text messages mark the connection's stages;
      // binary messages are the peer's bytes.
      socket.onmessage = (message) => {
        if (typeof message.data === "string") {
          const { t, message: why } = JSON.parse(message.data);
          if (t === "open") {
            events.onConnect();
          } else if (t === "end") {
            events.onEnd();
          } else if (t === "error") {
            close(new Error(why));
            socket.close();
          }
          return;
        }

        const data = new Uint8Array(message.data);
        if (paused) {
          held.push(data);
          return;
        }
        events.onData(data);
      };
      socket.onclose = () => close(null);
      socket.onerror = () => close(new Error(`connection to ${host}:${port} failed`));

      // Report once what was written has left, which is when the
      // guest's stack sends more.
      const watchDrain = () => {
        if (draining) {
          return;
        }
        draining = true;
        const look = () => {
          if (closed) {
            return;
          }
          if (socket.bufferedAmount > 0) {
            setTimeout(look, SEND_POLL_MS);
            return;
          }
          draining = false;
          events.onDrain();
        };
        setTimeout(look, SEND_POLL_MS);
      };

      return {
        write(data) {
          socket.send(data);
          watchDrain();
        },
        end() {
          if (socket.readyState === WebSocket.OPEN) {
            socket.send(JSON.stringify({ t: "end" }));
          }
        },
        destroy() {
          closed = true;
          socket.close();
        },
        pause() {
          paused = true;
        },
        resume() {
          paused = false;
          const waiting = held;
          held = [];
          for (const data of waiting) {
            events.onData(data);
          }
        },
        get buffered() {
          return socket.bufferedAmount;
        },
      };
    },
  };
}

// Open a connection to a port in the guest for the supervisor, which
// accepted one from outside and cannot reach into the guest itself. The
// page answers on a WebSocket that names the request, and copies bytes
// both ways. Either side finishing its sending is passed on as that:
// the other may still have an answer to send.
async function connectInbound(vm, id, port) {
  // The guest's side first. The supervisor sends the outside's bytes
  // the moment the WebSocket below opens, so everything that receives
  // them has to exist by then.
  let guest = null;
  let failure = null;
  try {
    guest = await vm.connect(port);
  } catch (error) {
    failure = error;
  }

  const url = new URL(INBOUND_URL, location.href).href.replace(/^http/, "ws");
  const socket = new WebSocket(`${url}?id=${encodeURIComponent(id)}`);
  socket.binaryType = "arraybuffer";

  // Nothing listens on the port, or the guest refused: the supervisor
  // is told, and drops the outside's connection.
  if (failure !== null) {
    socket.onopen = () => {
      socket.send(JSON.stringify({ t: "error", message: String(failure?.message ?? failure) }));
      socket.close();
    };
    return;
  }

  // Outside to guest, in order. The outside finishing its sending is
  // passed on as that, an end of input on the guest's side, and the
  // connection stays: the guest may still have an answer to send.
  const writer = guest.writable.getWriter();
  let writes = Promise.resolve();
  const ignore = () => {};
  socket.onmessage = (message) => {
    if (typeof message.data === "string") {
      writes = writes.then(() => writer.close()).catch(ignore);
      return;
    }
    const data = new Uint8Array(message.data);
    writes = writes.then(() => writer.write(data)).catch(ignore);
  };
  socket.onclose = () => guest.close();
  await new Promise((resolve, reject) => {
    socket.onopen = resolve;
    socket.onerror = () => reject(new Error("no inbound connection to the supervisor"));
  });

  // Guest to outside.
  const reader = guest.readable.getReader();
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) {
        break;
      }
      while (socket.bufferedAmount > SEND_HIGH_WATER) {
        await new Promise((resolve) => setTimeout(resolve, SEND_POLL_MS));
      }
      socket.send(value);
    }
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ t: "end" }));
    }
  } catch {
    socket.close();
  }
}

// Watch which ports the container's program listens on, and tell the
// supervisor, which listens on the same ports outside. Init prints a
// line per change, "+80" or "-80".
async function watchPorts(vm) {
  const watcher = await vm.spawn(`${BIN_DIR}/${INIT_NAME}`, [PORTS_FLAG], {
    pty: false,
    user: "root",
    cwd: "/",
  });
  watcher.stderr.cancel();

  const reader = watcher.stdout.pipeThrough(new TextDecoderStream()).getReader();
  let pending = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) {
      return;
    }
    pending += value;
    const lines = pending.split("\n");
    pending = lines.pop();
    for (const line of lines) {
      const port = Number(line.slice(1));
      if (!Number.isInteger(port) || port <= 0) {
        continue;
      }
      tell(line[0] === "+" ? ToSupervisor.LISTEN : ToSupervisor.UNLISTEN, { port });
    }
  }
}

// Send one chunk of a program's output, after waiting for the socket to
// have room: the program's output stream is only read as fast as the
// supervisor takes it.
async function sendOutput(stream, chunk) {
  show(stream, chunk);
  while (control.bufferedAmount > SEND_HIGH_WATER) {
    await new Promise((resolve) => setTimeout(resolve, SEND_POLL_MS));
  }
  const framed = new Uint8Array(chunk.length + 1);
  framed[0] = stream;
  framed.set(chunk, 1);
  control.send(framed);
}

// Forward everything a stream produces as `stream`'s output.
async function pump(readable, stream) {
  const reader = readable.getReader();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) {
      return;
    }
    await sendOutput(stream, value);
  }
}

// The exit status for how a program ended.
function statusOf(end) {
  if (end.type === END_EXIT) {
    return end.code;
  }
  if (end.type === END_SIGNAL) {
    return SIGNAL_STATUS_BASE + end.signal;
  }
  return LOST_STATUS;
}

// Run the container's program on a terminal. The supervisor's messages
// become the terminal's input, signals and size.
async function runOnTerminal(vm, config, incoming) {
  const pty = await vm.spawn(`${BIN_DIR}/${INIT_NAME}`, [`${BIN_DIR}/${SPEC_NAME}`], {
    user: "root",
    cwd: "/",
    cols: config.cols,
    rows: config.rows,
    term: config.term,
  });

  incoming.onInput = (bytes) => pty.write(bytes);
  // A terminal has no end of input to pass on: its program reads until
  // the program itself ends.
  incoming.onEof = () => {};
  incoming.onKill = (signal) => pty.kill(signal);
  incoming.onResize = (cols, rows) => pty.resize(cols, rows);

  await pump(pty.output, Stream.STDOUT);
  return statusOf(await pty.ended);
}

// Run the container's program on pipes.
async function runOnPipes(vm, incoming) {
  const program = await vm.spawn(`${BIN_DIR}/${INIT_NAME}`, [`${BIN_DIR}/${SPEC_NAME}`], {
    pty: false,
    user: "root",
    cwd: "/",
  });

  // Writes are chained so the bytes and the end of input reach the
  // program in the order they arrived.
  const stdin = program.stdin.getWriter();
  let writes = Promise.resolve();
  const ignore = () => {};
  incoming.onInput = (bytes) => {
    writes = writes.then(() => stdin.write(bytes)).catch(ignore);
  };
  incoming.onEof = () => {
    writes = writes.then(() => stdin.close()).catch(ignore);
  };
  incoming.onKill = (signal) => program.kill(signal);
  incoming.onResize = () => {};

  await Promise.all([pump(program.stdout, Stream.STDOUT), pump(program.stderr, Stream.STDERR)]);
  return statusOf(await program.ended);
}

async function main() {
  await new Promise((resolve, reject) => {
    control.onopen = resolve;
    control.onerror = () => reject(new Error("no connection to the supervisor"));
  });

  // What the supervisor sends before the program runs is kept, and
  // handed to the program's handlers once there are some.
  const incoming = {
    pending: [],
    onInput: null,
    onEof: null,
    onKill: null,
    onResize: null,
  };
  let started;
  const start = new Promise((resolve) => {
    started = resolve;
  });
  // The guest, once there is one, for requests that are about the guest
  // and not about the program.
  let guest = null;
  const dispatch = (message) => {
    if (message instanceof ArrayBuffer) {
      incoming.onInput(new Uint8Array(message).subarray(1));
      return;
    }
    const { t, signal, cols, rows } = JSON.parse(message);
    if (t === FromSupervisor.EOF) {
      incoming.onEof();
    } else if (t === FromSupervisor.KILL) {
      incoming.onKill(signal);
    } else if (t === FromSupervisor.RESIZE) {
      incoming.onResize(cols, rows);
    }
  };
  control.onmessage = (event) => {
    if (typeof event.data === "string") {
      const { t, id, port } = JSON.parse(event.data);
      if (t === FromSupervisor.START) {
        started();
        return;
      }
      if (t === FromSupervisor.CONNECT) {
        connectInbound(guest, id, port).catch((error) =>
          tell(ToSupervisor.LOG, { message: `inbound to port ${port}: ${error}` }),
        );
        return;
      }
    }
    if (incoming.onInput === null) {
      incoming.pending.push(event.data);
      return;
    }
    dispatch(event.data);
  };

  const [config, spec, init] = await Promise.all([
    fetchJson(CONFIG_URL),
    fetch(SPEC_URL).then((response) => response.text()),
    fetch(INIT_URL).then((response) => response.arrayBuffer()),
  ]);

  document.title = config.title;
  titleElement.textContent = config.title;
  setStatus(State.WAITING, "booting");

  // Init and the spec it reads, in a file system of their own.
  const bin = new MemoryFs();
  bin.writeFile(`/${INIT_NAME}`, new Uint8Array(init), { mode: EXECUTABLE });
  bin.writeFile(`/${SPEC_NAME}`, spec);

  // The log's timestamps are how a start-up's time is accounted for.
  tell(ToSupervisor.LOG, { message: "booting the kernel" });
  const vm = await createKernel({
    kernel: new URL("unkernel/unkernel.wasm", location.href).href,
    memoryMiB: config.memoryMiB,
    ...(config.network ? { net: supervisorNet() } : {}),
    mounts: {
      [BIN_DIR]: { fs: bin, readOnly: true },
      [LOWER_DIR]: { fs: imageFs(), readOnly: true, requestTimeout: NO_TIMEOUT },
    },
    log: (message) => tell(ToSupervisor.LOG, { message }),
  });

  // The guest is up and the image is mounted: the container is created.
  guest = vm;
  setStatus(State.WAITING, "created");
  tell(ToSupervisor.READY);
  await start;
  setStatus(State.RUNNING, "running");

  if (config.mirrorPorts) {
    watchPorts(vm).catch((error) => tell(ToSupervisor.LOG, { message: `port watcher: ${error}` }));
  }

  const running = config.terminal ? runOnTerminal(vm, config, incoming) : runOnPipes(vm, incoming);

  // The program's handlers are set by the first synchronous part of
  // the run, after the spawn; what arrived before goes to them now.
  const flush = setInterval(() => {
    if (incoming.onInput === null) {
      return;
    }
    clearInterval(flush);
    for (const message of incoming.pending.splice(0)) {
      dispatch(message);
    }
  }, SEND_POLL_MS);

  const status = await running;
  clearInterval(flush);
  setStatus(State.EXITED, `exited (${status})`);
  tell(ToSupervisor.EXIT, { status });
}

main().catch(fatal);
