#!/usr/bin/env python3
"""Record a tabc demo: a terminal on the left, a browser on the right.

Run under `asciinema rec`, which records what this prints as the
terminal's half. The browser's half is screenshots of a headless
Chromium, one every FRAME seconds on a fixed grid from the start, so
the two halves line up when laid side by side.

    python3 record.py <trynix tools dir> <scenario> <frames dir> <tabc bin dir> <work dir>
"""
import base64
import fcntl
import importlib.util
import os
import pty
import select
import struct
import sys
import tempfile
import termios
import threading
import time

TOOLS, SCENARIO, FRAMES, BIN, WORK = sys.argv[1:6]
FRAME = 0.1
TYPE_DELAY = 0.045
WIDTH, HEIGHT = 760, 470
URL_FILE = os.environ["TABC_DEMO_URL_FILE"]
PROMPT = "\033[1;32m$\033[0m "

spec = importlib.util.spec_from_file_location("cpu", os.path.join(TOOLS, "cpu-test.py"))
cpu = importlib.util.module_from_spec(spec)
sys.path.insert(0, TOOLS)
spec.loader.exec_module(cpu)

os.makedirs(FRAMES, exist_ok=True)
START = time.time()


TABS = []


class Tab:
    """One headless browser standing in for one tab."""

    def __init__(self):
        self.browser = cpu.Browser("chromium", os.path.join(tempfile.mkdtemp(), "profile"))
        TABS.append(self)
        self.lock = threading.Lock()
        self.send("Emulation.setDeviceMetricsOverride", width=WIDTH, height=HEIGHT,
                  deviceScaleFactor=1, mobile=False)

    def send(self, method, **params):
        with self.lock:
            return self.browser.send(method, **params)

    def go(self, url):
        self.send("Page.navigate", url=url)

    def click(self, x, y):
        for kind in ("mousePressed", "mouseReleased"):
            self.send("Input.dispatchMouseEvent", type=kind, x=x, y=y, button="left", clickCount=1)

    def type(self, text):
        for character in text:
            self.send("Input.insertText", text=character)
            time.sleep(TYPE_DELAY)
        self.send("Input.dispatchKeyEvent", type="keyDown", key="Enter", code="Enter",
                  windowsVirtualKeyCode=13, text="\r")
        self.send("Input.dispatchKeyEvent", type="keyUp", key="Enter", code="Enter",
                  windowsVirtualKeyCode=13)

    def shot(self):
        result = self.send("Page.captureScreenshot", format="png")
        return base64.b64decode((result.get("result") or result)["data"])


BLANK = "data:text/html,<body style='background:%2316181d'></body>"
shown = None
stop = False
marks = []


def mark(label):
    """Note when the browser's half changes what it shows, for captions."""
    marks.append((round(time.time() - START, 2), label))


def capture():
    index = 0
    last = None
    while not stop:
        due = START + index * FRAME
        delay = due - time.time()
        if delay > 0:
            time.sleep(delay)
        try:
            last = shown.shot()
        except Exception:
            pass
        # Every slot of the grid gets a frame, the last good one if this
        # one was missed, so the frame number is the time.
        behind = int((time.time() - START) / FRAME)
        while index <= behind:
            if last is not None:
                with open(os.path.join(FRAMES, f"f{index:05d}.png"), "wb") as out:
                    out.write(last)
            index += 1


def say(text):
    sys.stdout.write(text)
    sys.stdout.flush()


def type_command(text):
    say(PROMPT)
    for character in text:
        say(character)
        time.sleep(TYPE_DELAY)
    time.sleep(0.4)
    say("\r\n")


class Shell:
    """A command on a pty of its own, its output passed through."""

    def __init__(self, argv, env):
        size = os.get_terminal_size()
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(WORK)
            os.execvpe(argv[0], argv, env)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", size.lines, size.columns, 0, 0))
        self.seen = b""
        self.alive = True
        threading.Thread(target=self.pump, daemon=True).start()

    def pump(self):
        while True:
            try:
                ready, _, _ = select.select([self.fd], [], [], 0.2)
                if not ready:
                    continue
                data = os.read(self.fd, 65536)
            except OSError:
                break
            if not data:
                break
            self.seen += data
            os.write(1, data)
        self.alive = False

    def wait_for(self, text, timeout=60):
        deadline = time.time() + timeout
        needle = text.encode()
        while needle not in self.seen and time.time() < deadline:
            time.sleep(0.05)
        found = needle in self.seen
        self.seen = b""
        if not found:
            finish()
            raise SystemExit(f"never saw {text!r}")
        return found

    def type(self, text, enter=True):
        for character in text:
            os.write(self.fd, character.encode())
            time.sleep(TYPE_DELAY)
        if enter:
            time.sleep(0.3)
            os.write(self.fd, b"\r")

    def wait_exit(self, timeout=30):
        deadline = time.time() + timeout
        while self.alive and time.time() < deadline:
            time.sleep(0.1)


def wait_url(timeout=60):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if os.path.exists(URL_FILE) and os.path.getsize(URL_FILE) > 0:
            return open(URL_FILE).read().strip()
        time.sleep(0.05)
    raise SystemExit("no address was opened")


ENV = dict(os.environ)


def scenario_tab():
    """One container in a tab: typed into from both sides, then serving."""
    global shown
    container = Tab()
    container.go(BLANK)
    served = Tab()
    served.go(BLANK)
    shown = container
    threading.Thread(target=capture, daemon=True).start()
    mark("no tab yet")

    time.sleep(1.0)
    command = "tabc run -it --open -p 8123:80 nginx:alpine sh"
    type_command(command)
    shell = Shell([os.path.join(BIN, "tabc")] + command.split()[1:], ENV)
    container.go(wait_url())
    mark("the container, in a tab")
    shell.wait_for("# ")
    time.sleep(1.2)

    shell.type("uname -sm")
    time.sleep(1.6)
    container.click(300, 200)
    container.type("echo typed in the tab")
    time.sleep(1.8)
    shell.type("echo typed in the terminal")
    time.sleep(1.8)
    shell.type("cd /usr/share/nginx/html")
    time.sleep(0.6)
    shell.type("echo '<h1>Served from a browser tab</h1>' > index.html")
    time.sleep(0.8)
    shell.type("nginx")
    time.sleep(2.0)

    served.go("http://127.0.0.1:8123/")
    time.sleep(1.0)
    shown = served
    mark("a second tab, at localhost:8123")
    time.sleep(3.5)
    shown = container
    mark("the container, in a tab")
    time.sleep(2.5)
    shell.type("exit")
    shell.wait_exit()
    time.sleep(1.0)


def scenario_compose():
    """Compose under the dashboard: every service a pane."""
    global shown
    dashboard = Tab()
    dashboard.go(BLANK)
    shown = dashboard
    threading.Thread(target=capture, daemon=True).start()
    mark("no dashboard yet")
    tocker = os.path.join(BIN, "tocker")

    time.sleep(1.0)
    type_command("grep -A1 -E '^  [a-z]+:$' compose.yaml")
    Shell(["sh", "-c", "grep -A1 -E '^  [a-z]+:$' compose.yaml"], ENV).wait_exit()
    time.sleep(1.5)

    type_command("tocker --dashboard compose up -d")
    up = Shell([tocker, "--dashboard", "compose", "up", "-d"], ENV)
    dashboard.go(wait_url())
    mark("the dashboard")
    up.wait_exit(120)

    # The web server takes a few seconds to import its modules under
    # emulation. The demo asks once it answers, as a person would after
    # seeing its "listening" line in the pane.
    import subprocess
    deadline = time.time() + 60
    while time.time() < deadline:
        probe = subprocess.run(["curl", "-s", "-m", "2", "-o", "/dev/null", "-w", "%{http_code}",
                                "http://127.0.0.1:8123/healthz-not-counted"], capture_output=True, text=True)
        if probe.stdout.strip() not in ("", "000"):
            break
        time.sleep(0.3)
    time.sleep(1.0)

    for _ in range(3):
        type_command("curl localhost:8123")
        Shell(["curl", "-s", "-m", "10", "http://127.0.0.1:8123/"], ENV).wait_exit()
        time.sleep(1.3)
    time.sleep(1.5)

    type_command("tocker compose down")
    Shell([tocker, "compose", "down", "-t", "2"], ENV).wait_exit(120)
    time.sleep(1.5)
    Shell([tocker, "--stop"], ENV).wait_exit()


def finish():
    """Stop the frames and the browsers, which would otherwise hold the
    recording's terminal open."""
    global stop
    stop = True
    time.sleep(0.3)
    with open(os.path.join(FRAMES, "marks.txt"), "w") as out:
        for when, label in marks:
            out.write(f"{when}\t{label}\n")
    for tab in TABS:
        try:
            tab.browser.close()
        except Exception:
            pass


{"tab": scenario_tab, "compose": scenario_compose}[SCENARIO]()
finish()
