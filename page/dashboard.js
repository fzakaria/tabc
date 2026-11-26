// The dashboard: one pane for every container the daemon runs.
//
// The daemon cannot start a container by itself in this mode. A
// container is a page that boots a guest, and here that page is a frame
// on this one. So the dashboard asks the daemon what containers there
// are, and gives each a pane whose frame loads the container's page.
// The daemon's `start` returns when the frame's guest is up.

// Where the daemon lists its containers, and how often it is asked.
const LIST_URL = "containers.json";
const POLL_MS = 500;

// A container's states, as the daemon names them, which are also the
// status dot's colours (dashboard.html).
const State = Object.freeze({
  CREATED: "created",
  RUNNING: "running",
  EXITED: "exited",
});

const panesElement = document.getElementById("panes");
const emptyElement = document.getElementById("empty");
const summaryElement = document.getElementById("summary");

// Each container's pane, by the container's id.
const panes = new Map();

// Make the pane for a container: a bar with its name, its image, a link
// for each port it publishes and its state, over the frame that is the
// container.
function makePane(container) {
  const section = document.createElement("section");
  const bar = document.createElement("header");

  const name = document.createElement("span");
  name.className = "name";
  name.textContent = container.name;

  const image = document.createElement("span");
  image.className = "image";
  image.textContent = container.image;

  const ports = document.createElement("span");
  ports.className = "ports";
  for (const { host } of container.ports) {
    const link = document.createElement("a");
    link.href = `http://localhost:${host}/`;
    link.target = "_blank";
    link.rel = "noreferrer";
    link.textContent = `:${host}`;
    ports.append(link);
  }

  const status = document.createElement("span");
  status.className = "status";

  const frame = document.createElement("iframe");
  frame.title = container.name;

  bar.append(name, image, ports, status);
  section.append(bar, frame);
  panesElement.append(section);
  return { section, status, frame, url: "" };
}

// What a pane's status says for a container.
function statusText(container) {
  if (container.status === State.EXITED) {
    return `exited (${container.exitCode})`;
  }
  if (container.status === State.RUNNING) {
    return "running";
  }
  return container.url === "" ? "created" : "booting";
}

// Bring the panes in line with the daemon's list: a pane for each new
// container, the frame loaded once the container has a page, the state
// kept current, and the pane gone when the container is.
function update(containers) {
  const present = new Set();
  for (const container of containers) {
    present.add(container.id);
    if (!panes.has(container.id)) {
      panes.set(container.id, makePane(container));
    }

    const pane = panes.get(container.id);
    pane.status.dataset.state = container.status;
    pane.status.textContent = statusText(container);

    // The page's address appears when the container is started, and a
    // container started again has a new one.
    if (container.url !== "" && container.url !== pane.url) {
      pane.url = container.url;
      pane.frame.src = container.url;
    }
  }

  for (const [id, pane] of panes) {
    if (!present.has(id)) {
      pane.section.remove();
      panes.delete(id);
    }
  }

  emptyElement.hidden = containers.length > 0;
  const running = containers.filter((container) => container.status === State.RUNNING).length;
  summaryElement.textContent =
    containers.length === 0
      ? "no containers"
      : `${containers.length} container${containers.length === 1 ? "" : "s"}, ${running} running, each in a tab of its own`;
}

async function poll() {
  try {
    const response = await fetch(LIST_URL, { cache: "no-store" });
    if (response.ok) {
      update(await response.json());
      summaryElement.dataset.lost = "";
    }
  } catch {
    // The daemon went away. The panes stay as they were, and say so.
    summaryElement.textContent = "the daemon is not answering";
  }
  setTimeout(poll, POLL_MS);
}

poll();
