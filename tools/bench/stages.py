#!/usr/bin/env python3
"""Time the stages of a container's start from the supervisor's log.

    python3 stages.py <tabc> <bundle dir> <runs> <out.json>

Each run is create, start, wait, with the log read before delete.
"""
import datetime
import json
import os
import subprocess
import sys
import tempfile
import time

TABC, BUNDLE, RUNS, OUT = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
MARKS = [
    ("supervising", "supervisor up"),
    ("browser started", "browser launched"),
    ("page connected", "page connected"),
    ("booting the kernel", "kernel boot begins"),
    ("created", "guest ready"),
    ("running", "start received"),
    ("exited with", "program exited"),
]


def stamp(line):
    return datetime.datetime.strptime(line[:26], "%Y/%m/%d %H:%M:%S.%f").timestamp()


rows = []
root = tempfile.mkdtemp(prefix="tabc-bench-", dir="/tmp")
for run in range(RUNS):
    name = f"b{run}"
    began = time.time()
    subprocess.run([TABC, "--root", root, "create", "--bundle", BUNDLE, name],
                   check=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL)
    subprocess.run([TABC, "--root", root, "start", name], check=True)
    log = os.path.join(root, name, "supervisor.log")
    while True:
        state = json.loads(subprocess.run([TABC, "--root", root, "state", name],
                                          check=True, capture_output=True).stdout)
        if state["status"] == "stopped":
            break
        time.sleep(0.005)
    ended = time.time()
    times = {}
    for line in open(log, errors="replace"):
        for needle, label in MARKS:
            if needle in line and label not in times and line[:4].isdigit():
                times[label] = stamp(line)
    subprocess.run([TABC, "--root", root, "delete", name], check=True)
    row = {"run": run, "command": began, "end": ended, **times}
    rows.append(row)
    print({k: round(v - began, 3) for k, v in row.items() if k not in ("run", "command")}, flush=True)

json.dump(rows, open(OUT, "w"), indent=1)
