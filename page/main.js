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

// Where the guest finds the image, and init with the container's spec.
const LOWER_DIR = "/tabc/lower";
const BIN_DIR = "/tabc/bin";
const INIT_NAME = "init";
const SPEC_NAME = "spec.json";
const EXECUTABLE = 0o755;

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
});
const ToSupervisor = Object.freeze({
  READY: "ready",
  EXIT: "exit",
  LOG: "log",
  ERROR: "error",
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

  return {
    getattr: async (path) => (await call("attr", path)).json(),
    readdir: async (path) => (await call("dir", path)).json(),
    readlink: async (path) => (await (await call("link", path)).json()).target,
    open: async (path, flags) => {
      if (flags.write) {
        throw new FsError("EROFS");
      }

      // Opening checks the file is there; each read names its range.
      await call("attr", path);
      return {
        read: async (offset, length) =>
          new Uint8Array(
            await (await call("read", path, `&o=${offset}&n=${length}`)).arrayBuffer(),
          ),
        getattr: async () => (await call("attr", path)).json(),
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

// Send one chunk of a program's output, after waiting for the socket to
// have room: the program's output stream is only read as fast as the
// supervisor takes it.
async function sendOutput(stream, chunk) {
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
    if (typeof event.data === "string" && JSON.parse(event.data).t === FromSupervisor.START) {
      started();
      return;
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

  // Init and the spec it reads, in a file system of their own.
  const bin = new MemoryFs();
  bin.writeFile(`/${INIT_NAME}`, new Uint8Array(init), { mode: EXECUTABLE });
  bin.writeFile(`/${SPEC_NAME}`, spec);

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
  tell(ToSupervisor.READY);
  await start;

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
  tell(ToSupervisor.EXIT, { status });
}

main().catch(fatal);
