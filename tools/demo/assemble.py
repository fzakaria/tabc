#!/usr/bin/env python3
"""Lay a recorded terminal beside the browser's frames as one video.

    python3 assemble.py <cast> <frames dir> <font.ttf> <out basename> <terminal label>
"""
import subprocess
import sys

CAST, FRAMES, FONT, OUT, LABEL = sys.argv[1:6]
FPS = 10
HEIGHT = 470
STRIP = 30
GROUND = "0x16181d"
INK = "0xd7dae0"

# The terminal's half: every frame at its own time, no idle time cut,
# since the browser's half is on the same clock.
subprocess.run(["agg", "--font-size", "14", "--theme", "monokai", "--fps-cap", str(FPS),
                "--idle-time-limit", "100000", "--last-frame-duration", "1", CAST, OUT + "-term.gif"],
               check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

# What the browser's half is showing, by time, as the recorder noted it.
marks = [line.rstrip("\n").split("\t") for line in open(FRAMES + "/marks.txt")]
labels = []
for index, (start, text) in enumerate(marks):
    end = marks[index + 1][0] if index + 1 < len(marks) else "100000"
    text = text.replace(":", "\\:").replace("'", "")
    labels.append(
        f"drawtext=fontfile={FONT}:text='{text}':fontcolor={INK}:fontsize=15:x=12:y=8:"
        f"enable='between(t,{start},{end})'"
    )

strip = f"pad=iw:ih+{STRIP}:0:{STRIP}:color={GROUND}"
graph = (
    f"[0:v]fps={FPS},scale=-2:{HEIGHT}:flags=lanczos,{strip},"
    f"drawtext=fontfile={FONT}:text='{LABEL}':fontcolor={INK}:fontsize=15:x=12:y=8[term];"
    f"[1:v]scale=-2:{HEIGHT}:flags=lanczos,{strip},{','.join(labels)}[tab];"
    f"[term][tab]hstack=inputs=2,pad=iw+16:ih+16:8:8:color={GROUND},"
    f"scale=1280:-2:flags=lanczos,format=yuv420p[out]"
)
subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-i", OUT + "-term.gif",
                "-framerate", str(FPS), "-i", FRAMES + "/f%05d.png",
                "-filter_complex", graph, "-map", "[out]", "-c:v", "libx264", "-crf", "24",
                "-preset", "slow", "-movflags", "+faststart", OUT + ".mp4"], check=True)

# An animated WebP of the same, for places that show images and not
# videos.
subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-i", OUT + ".mp4",
                "-vf", "fps=8,scale=1000:-2:flags=lanczos", "-c:v", "libwebp", "-lossless", "0",
                "-q:v", "55", "-loop", "0", "-an", OUT + ".webp"], check=True)
