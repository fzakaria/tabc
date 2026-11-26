// What a container's page (main.js) and the worker that runs the
// container's guest (guest.js) say to each other, by each message's `t`
// field.

// From the worker: the container's name, how the container is doing,
// the terminal to make, a chunk to write on the terminal, and the
// terminal's new size.
export const ToPage = Object.freeze({
  TITLE: "title",
  STATUS: "status",
  OPEN: "open",
  SHOW: "show",
  RESIZE: "resize",
});

// From the page: what was typed into the terminal, as bytes.
export const ToGuest = Object.freeze({
  TYPED: "typed",
});

// How a container is doing. The states are the status dot's colours
// (index.html).
export const State = Object.freeze({
  WAITING: "waiting",
  RUNNING: "running",
  EXITED: "exited",
  FAILED: "failed",
});
