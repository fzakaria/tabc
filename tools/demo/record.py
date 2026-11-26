#!/usr/bin/env python3
"""Record a tabc demo: a terminal, and under it a real browser window.

Run under `asciinema rec`, which records what this prints as the
terminal's half. The browser's half is a Chromium window on a virtual
display, with its tabs and its address bar, recorded off the display by
ffmpeg. Both halves are on one clock: this notes how long after its own
start the screen recording began, and the two are lined up by that when
they are put together (assemble.py).

    python3 record.py <scenario> <out dir> <tabc bin dir> <work dir>
"""
import fcntl
import json
import os
import pty
import select
import shutil
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time
import urllib.parse
import urllib.request

import websocket

BEGAN = time.time()
SCENARIO, OUT, BIN, WORK = sys.argv[1:5]
TYPE_DELAY = 0.045
PROMPT = "\033[1;32m$\033[0m "
URL_FILE = os.environ["TABC_DEMO_URL_FILE"]
DEBUG_PORT = int(os.environ["TABC_DEMO_DEBUG_PORT"])
DISPLAY = os.environ.get("TABC_DEMO_DISPLAY", ":97")
FPS = 10

# The window in the browser's own units, and how many pixels each is
# drawn with: the recording is twice as wide as it is shown, so type
# stays sharp.
SCALE = 2.5
WINDOW_WIDTH = 544
WINDOW_HEIGHT = int(os.environ.get("DEMO_WINDOW_HEIGHT", "360"))
SCREEN = f"{int(WINDOW_WIDTH * SCALE)}x{int(WINDOW_HEIGHT * SCALE)}"

os.makedirs(OUT, exist_ok=True)
children = []


def start(argv, **kwargs):
    process = subprocess.Popen(argv, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, **kwargs)
    children.append(process)
    return process


# A display nobody is looking at, a real browser window on it, and a
# recording of the display. The browser is told to use X: left to
# itself on a Wayland desktop it would open on the desktop.
start(["Xvfb", DISPLAY, "-screen", "0", f"{SCREEN}x24", "-nolisten", "tcp"])
time.sleep(0.8)
browser_env = {k: v for k, v in os.environ.items() if k != "WAYLAND_DISPLAY"}
browser_env["DISPLAY"] = DISPLAY
start(["chromium", "--ozone-platform=x11", f"--user-data-dir={tempfile.mkdtemp()}",
       "--no-first-run", "--no-default-browser-check", "--disable-features=Translate",
       f"--force-device-scale-factor={SCALE}", "--window-position=0,0",
       f"--window-size={WINDOW_WIDTH},{WINDOW_HEIGHT}",
       f"--remote-debugging-port={DEBUG_PORT}", "--remote-allow-origins=*", "about:blank"],
      env=browser_env)


def debug(path, method="GET"):
    request = urllib.request.Request(f"http://127.0.0.1:{DEBUG_PORT}{path}", method=method)
    with urllib.request.urlopen(request, timeout=5) as response:
        body = response.read()
    # Some of the browser's answers are a plain sentence, not JSON.
    try:
        return json.loads(body)
    except ValueError:
        return None


deadline = time.time() + 30
while True:
    try:
        debug("/json/version")
        break
    except Exception:
        if time.time() > deadline:
            raise SystemExit("the browser did not start")
        time.sleep(0.2)
time.sleep(1.0)

recorder = subprocess.Popen(
    ["ffmpeg", "-y", "-loglevel", "error", "-f", "x11grab", "-framerate", str(FPS),
     "-video_size", SCREEN, "-i", DISPLAY, "-c:v", "libx264", "-preset", "ultrafast",
     "-crf", "0", os.path.join(OUT, "browser.mkv")],
    stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
time.sleep(0.6)
with open(os.path.join(OUT, "offset.txt"), "w") as out:
    out.write(f"{time.time() - BEGAN - 0.3:.2f}\n")


def tab_for(url):
    """The tab showing an address, once the browser has one."""
    deadline = time.time() + 30
    while time.time() < deadline:
        for target in debug("/json/list"):
            if target.get("type") == "page" and target.get("url", "").startswith(url):
                return target
        time.sleep(0.1)
    raise SystemExit(f"no tab for {url}")


def new_tab(url):
    return debug("/json/new?" + url, method="PUT")


def activate(target):
    debug("/json/activate/" + target["id"])


class Page:
    """A tab, to click in and type into."""

    def __init__(self, target):
        self.socket = websocket.create_connection(target["webSocketDebuggerUrl"])
        self.next = 0

    def send(self, method, **params):
        self.next += 1
        self.socket.send(json.dumps({"id": self.next, "method": method, "params": params}))
        while True:
            answer = json.loads(self.socket.recv())
            if answer.get("id") == self.next:
                return answer

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


def finish():
    """Stop the screen recording and everything started here, which
    would otherwise hold the recording's terminal open."""
    try:
        recorder.stdin.write(b"q")
        recorder.stdin.flush()
        recorder.wait(timeout=10)
    except Exception:
        recorder.kill()
    for process in reversed(children):
        process.kill()


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

    def type(self, text):
        for character in text:
            os.write(self.fd, character.encode())
            time.sleep(TYPE_DELAY)
        time.sleep(0.3)
        os.write(self.fd, b"\r")

    def wait_exit(self, timeout=30):
        deadline = time.time() + timeout
        while self.alive and time.time() < deadline:
            time.sleep(0.1)


def opened(timeout=60):
    """The address the stand-in opener was last asked to open."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        if os.path.exists(URL_FILE) and os.path.getsize(URL_FILE) > 0:
            return open(URL_FILE).read().strip()
        time.sleep(0.05)
    finish()
    raise SystemExit("no address was opened")


ENV = dict(os.environ)


def scenario_tab():
    """One container in a tab: typed into from both sides, then serving
    a page to a second tab."""
    time.sleep(1.0)
    command = "tabc run -it --open -p 8123:80 nginx:alpine sh"
    type_command(command)
    shell = Shell([os.path.join(BIN, "tabc")] + command.split()[1:], ENV)
    container = tab_for(opened())
    page = Page(container)
    shell.wait_for("# ")
    time.sleep(1.2)

    shell.type("uname -sm")
    time.sleep(1.6)
    page.click(200, 100)
    page.type("echo typed in the tab")
    time.sleep(1.8)
    shell.type("echo typed in the terminal")
    time.sleep(1.8)
    shell.type("cd /usr/share/nginx/html")
    time.sleep(0.6)
    shell.type("echo '<h1>Served from a browser tab</h1>' > index.html")
    time.sleep(0.8)
    shell.type("nginx")
    time.sleep(2.0)

    new_tab("http://localhost:8123/")
    time.sleep(4.0)
    activate(container)
    time.sleep(2.5)
    shell.type("exit")
    shell.wait_exit()
    time.sleep(1.0)


def scenario_compose():
    """Compose under the dashboard: every service a pane."""
    tocker = os.path.join(BIN, "tocker")

    time.sleep(1.0)
    type_command("grep -A1 -E '^  [a-z]+:$' compose.yaml")
    Shell(["sh", "-c", "grep -A1 -E '^  [a-z]+:$' compose.yaml"], ENV).wait_exit()
    time.sleep(1.5)

    type_command("tocker --dashboard compose up -d")
    up = Shell([tocker, "--dashboard", "compose", "up", "-d"], ENV)
    up.wait_exit(120)

    dashboard = tab_for(opened())

    # The web server takes a few seconds to import its modules under
    # emulation. The demo asks once the server answers. The probe is
    # for a path the server does not count as a visit.
    deadline = time.time() + 60
    while time.time() < deadline:
        probe = subprocess.run(["curl", "-s", "-m", "2", "-o", "/dev/null", "-w", "%{http_code}",
                                "http://127.0.0.1:8123/ready"], capture_output=True, text=True)
        if probe.stdout.strip() not in ("", "000"):
            break
        time.sleep(0.3)
    time.sleep(1.5)

    # The service is visited from the same browser, in a tab beside the
    # one the containers are running in, and then from the terminal.
    visitor = Page(new_tab("http://localhost:8123/"))
    time.sleep(2.5)
    for _ in range(2):
        visitor.send("Page.reload")
        time.sleep(1.8)
    activate(dashboard)
    time.sleep(3.0)

    type_command("curl localhost:8123")
    Shell(["curl", "-s", "-m", "10", "http://127.0.0.1:8123/"], ENV).wait_exit()
    time.sleep(3.0)

    type_command("tocker compose down")
    Shell([tocker, "compose", "down", "-t", "2"], ENV).wait_exit(120)
    time.sleep(1.5)
    Shell([tocker, "--stop"], ENV).wait_exit()


try:
    {"tab": scenario_tab, "compose": scenario_compose}[SCENARIO]()
finally:
    finish()
