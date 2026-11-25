#!/usr/bin/env python3
"""Host memory one idle container costs: the proportional set size of
every process in the supervisor's session, browser included.

    python3 memory.py <tabc> <out.json>
"""
import json
import os
import subprocess
import sys
import time

TABC, OUT = sys.argv[1], sys.argv[2]


def pss_kb(pid):
    try:
        for line in open(f"/proc/{pid}/smaps_rollup"):
            if line.startswith("Pss:"):
                return int(line.split()[1])
    except OSError:
        pass
    return 0


def session_of(pid):
    try:
        return int(open(f"/proc/{pid}/stat").read().rsplit(")", 1)[1].split()[3])
    except OSError:
        return -1


def measure(extra):
    proc = subprocess.Popen([TABC, "run", *extra, "alpine:3.22", "sleep", "40"],
                            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL)
    time.sleep(12)
    # The supervisor is the child of `tabc run` and leads its own session.
    children = subprocess.run(["pgrep", "-P", str(proc.pid)], capture_output=True, text=True).stdout.split()
    supervisor = int(children[0])
    members = [int(p) for p in os.listdir("/proc") if p.isdigit() and session_of(p) == supervisor]
    rows = []
    for pid in members:
        try:
            name = open(f"/proc/{pid}/cmdline").read().split("\0")
        except OSError:
            continue
        # Chromium rewrites a child's command line into one string, so
        # the type is searched for in the text, not the argument list.
        text = " ".join(name)
        kind = "supervisor"
        if "--type=" in text:
            kind = text.split("--type=", 1)[1].split()[0]
        elif "chrom" in text:
            kind = "browser"
        rows.append({"pid": pid, "kind": kind, "pss_mib": pss_kb(pid) / 1024})
    proc.terminate()
    proc.wait()
    return rows


out = {"default (1 GiB guest)": measure([]), "-m 256m": measure(["-m", "256m"])}
for name, rows in out.items():
    total = sum(r["pss_mib"] for r in rows)
    by = {}
    for r in rows:
        by[r["kind"]] = by.get(r["kind"], 0) + r["pss_mib"]
    print(name, f"total {total:.0f} MiB", {k: round(v) for k, v in sorted(by.items(), key=lambda kv: -kv[1])})
json.dump(out, open(OUT, "w"), indent=1)
