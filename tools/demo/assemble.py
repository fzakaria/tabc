#!/usr/bin/env python3
"""Put a recorded terminal above the recording of the browser window.

    python3 assemble.py <cast> <out dir of record.py> <out basename> [terminal font size]

One half above the other, each the full width. Side by side, each half
is too narrow to read in a column of text.
"""
import subprocess
import sys

CAST, RECORDED, OUT = sys.argv[1:4]
TERMINAL_FONT = sys.argv[4] if len(sys.argv) > 4 else "34"
FPS = 10
# The finished video's width in pixels: twice the width it is shown at.
WIDTH = 1360
GROUND = "0x16181d"
GAP = 12

# How long after the terminal's recording began the screen's did.
offset = open(RECORDED + "/offset.txt").read().strip()

# The terminal: every frame at its own time, no idle time cut, since the
# browser's half is on the same clock.
subprocess.run(["agg", "--font-size", TERMINAL_FONT, "--theme", "monokai", "--fps-cap", str(FPS),
                "--idle-time-limit", "100000", "--last-frame-duration", "1", CAST, OUT + "-term.gif"],
               check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

graph = (
    f"[0:v]fps={FPS},scale={WIDTH}:-2:flags=lanczos,pad=iw:ih+{GAP}:0:0:color={GROUND}[term];"
    f"[1:v]fps={FPS},scale={WIDTH}:-2:flags=lanczos[window];"
    f"[term][window]vstack=inputs=2:shortest=1,format=yuv420p[out]"
)
subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-ss", offset, "-i", OUT + "-term.gif",
                "-i", RECORDED + "/browser.mkv", "-filter_complex", graph, "-map", "[out]",
                "-c:v", "libx264", "-crf", "18", "-preset", "slow", "-movflags", "+faststart",
                OUT + ".mp4"], check=True)
