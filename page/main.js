// One container in one tab: what the tab shows.
//
// The container itself, a Linux guest and the program in it, runs in a
// worker (guest.js), which is also what talks to the supervisor. This
// page shows what the worker tells it to: the container's name, how the
// container is doing, and the terminal the program runs on. What is
// typed into the terminal goes back to the worker.

import { State, ToGuest, ToPage } from "./messages.js";

// The worker's script, next to this page.
const GUEST_URL = "guest.js";

// The parts of the page (index.html).
const titleElement = document.getElementById("title");
const statusElement = document.getElementById("status");
const terminalElement = document.getElementById("terminal");

// The terminal's look, matching the page around it, and how much of
// what scrolled away it keeps.
const TERMINAL_THEME = { background: "#16181d", foreground: "#d7dae0", cursor: "#d7dae0" };
const TERMINAL_FONT_SIZE = 13;
const TERMINAL_SCROLLBACK = 5000;

// The escape sequence that prints what follows in red, and the one that
// ends it, for what the page itself has to say.
const RED = "\x1b[31m";
const PLAIN = "\x1b[0m";

function setStatus(state, text) {
  statusElement.dataset.state = state;
  statusElement.textContent = text;
}

// The terminal on the page. It shows what the program prints, and for a
// program that was given a terminal it takes what is typed into the
// page, so a container opened in a browser is used like one opened in a
// terminal. Made once the container's configuration is known; what is
// written before that is kept and written then.
let terminal = null;
const early = [];

function show(chunk) {
  if (terminal === null) {
    early.push(chunk);
    return;
  }
  terminal.write(chunk);
}

// Make the terminal for a container. `onType` is given what is typed.
function openTerminal(config, onType) {
  terminal = new Terminal({
    cols: config.cols,
    rows: config.rows,
    // A program on pipes ends its lines with a bare newline, which a
    // terminal takes to mean "down" and not "down and back".
    convertEol: !config.terminal,
    disableStdin: !config.terminal,
    cursorBlink: config.terminal,
    fontSize: TERMINAL_FONT_SIZE,
    scrollback: TERMINAL_SCROLLBACK,
    theme: TERMINAL_THEME,
  });
  terminal.open(terminalElement);

  // A program on pipes has no size of its own, so its output takes the
  // size of the page. A program on a terminal was told a size, and the
  // page keeps to it.
  if (!config.terminal) {
    const fitter = new FitAddon.FitAddon();
    terminal.loadAddon(fitter);
    fitter.fit();
    addEventListener("resize", () => fitter.fit());
  } else {
    const encoder = new TextEncoder();
    terminal.onData((typed) => onType(encoder.encode(typed)));
    terminal.focus();
  }

  for (const chunk of early.splice(0)) {
    terminal.write(chunk);
  }
}

// Inside a dashboard's pane the dashboard already says what the
// container is and how it is doing, so the page's own bar is dropped.
if (window.top !== window) {
  document.body.classList.add("embedded");
}

const guest = new Worker(new URL(GUEST_URL, location.href), { type: "module" });

guest.onmessage = ({ data }) => {
  switch (data.t) {
    case ToPage.TITLE:
      document.title = data.title;
      titleElement.textContent = data.title;
      break;
    case ToPage.STATUS:
      setStatus(data.state, data.text);
      break;
    case ToPage.OPEN:
      openTerminal(data, (bytes) => guest.postMessage({ t: ToGuest.TYPED, bytes }));
      break;
    case ToPage.SHOW:
      show(data.chunk);
      break;
    case ToPage.RESIZE:
      terminal?.resize(data.cols, data.rows);
      break;
  }
};

// A worker whose script did not load or run has no way to tell the
// supervisor, which gives up on the page in its own time. The page can
// at least show what happened.
guest.onerror = (event) => {
  setStatus(State.FAILED, "failed");
  show(`\r\n${RED}${event.message ?? "the container's worker failed to start"}${PLAIN}\r\n`);
};
