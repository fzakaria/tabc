#!/usr/bin/env python3
"""Start-up times and CPU costs, tabc against runc, through one Docker
daemon. Results as JSON.

    python3 work.py <docker socket> <out.json>
"""
import json
import statistics
import subprocess
import sys
import time

SOCKET, OUT = sys.argv[1], sys.argv[2]
DOCKER = ["docker", "-H", f"unix://{SOCKET}"]
RUNS = 10

PY_LOOP = "import time; t=time.perf_counter(); sum(i*i for i in range(5_000_000)); print(time.perf_counter()-t)"
TIMED = "import subprocess, time; t=time.perf_counter(); subprocess.run({!r}, shell=True, check=True); print(time.perf_counter()-t)"
IMAGE = "python:3.13-alpine"
WORK = {
    "python: sum of 5M squares": (IMAGE, ["python", "-c", PY_LOOP], 1.0),
    "sha256sum of 64 MB": (IMAGE, ["python", "-c", TIMED.format("head -c 67108864 /dev/zero | sha256sum >/dev/null")], 1.0),
    "gzip of 32 MB": (IMAGE, ["python", "-c", TIMED.format("head -c 33554432 /dev/urandom | gzip >/dev/null")], 1.0),
    "find over /usr": (IMAGE, ["python", "-c", TIMED.format("find /usr | wc -l >/dev/null")], 1.0),
    "python: import json, parse 1 MB": (IMAGE, ["python", "-c", "import time; t=time.perf_counter(); import json; json.loads(json.dumps(list(range(150000)))); print(time.perf_counter()-t)"], 1.0),
}
KINDS = {
    "runc": ["--platform", "linux/amd64"],
    "tabc": ["--runtime", "tabc", "--platform", "linux/arm64"],
}


def docker(args, **kw):
    return subprocess.run(DOCKER + args, capture_output=True, text=True, **kw)


results = {"startup": {}, "work": {}}

# Images first, so no run pays for a pull.
for image in {"alpine:3.22", "python:3.13-alpine"}:
    for platform in ("linux/amd64", "linux/arm64"):
        docker(["pull", "-q", "--platform", platform, image])

for kind, flags in KINDS.items():
    times = []
    for _ in range(RUNS + 1):
        began = time.perf_counter()
        out = docker(["run", "--rm", *flags, "alpine:3.22", "echo", "hi"])
        assert out.stdout.strip() == "hi", out.stderr
        times.append(time.perf_counter() - began)
    results["startup"][kind] = times[1:]
    print(kind, "startup median", round(statistics.median(times[1:]), 3), flush=True)

for name, (image, command, scale) in WORK.items():
    results["work"][name] = {}
    for kind, flags in KINDS.items():
        seconds = []
        for _ in range(3):
            out = docker(["run", "--rm", *flags, image, *command])
            seconds.append(float(out.stdout.strip().splitlines()[-1]) * scale)
        results["work"][name][kind] = seconds
        print(name, kind, [round(s, 3) for s in seconds], flush=True)

json.dump(results, open(OUT, "w"), indent=1)
