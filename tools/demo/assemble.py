#!/usr/bin/env python3
"""Lay a recorded terminal beside the browser's frames as one video.

    python3 assemble.py <cast> <frames dir> <font.ttf> <out basename> <terminal label> [terminal font size]
"""
import subprocess
import sys

CAST, FRAMES, FONT, OUT, LABEL = sys.argv[1:6]
FPS = 10
# The finished video's width in pixels: twice the width a blog column
# shows it at, so type stays sharp on a dense screen.
WIDTH = 1360
STRIP = 44
GROUND = "0x16181d"
INK = "0xd7dae0"
MUTED = "0x8b91a0"
LABEL_SIZE = 24
# The terminal's type, sized so its columns fill the width.
TERMINAL_FONT = sys.argv[6] if len(sys.argv) > 6 else "34"

# The terminal's half: every frame at its own time, no idle time cut,
# since the browser's half is on the same clock.
subprocess.run(["agg", "--font-size", TERMINAL_FONT, "--theme", "monokai", "--fps-cap", str(FPS),
                "--idle-time-limit", "100000", "--last-frame-duration", "1", CAST, OUT + "-term.gif"],
               check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

# What the browser's half is showing, by time, as the recorder noted it.
marks = [line.rstrip("\n").split("\t") for line in open(FRAMES + "/marks.txt")]
labels = []
for index, (start, text) in enumerate(marks):
    end = marks[index + 1][0] if index + 1 < len(marks) else "100000"
    text = text.replace(":", "\\:").replace("'", "")
    labels.append(
        f"drawtext=fontfile={FONT}:text='{text}':fontcolor={MUTED}:fontsize={LABEL_SIZE}:x=16:y=10:"
        f"enable='between(t,{start},{end})'"
    )

# One half above the other, each the full width with a strip that says
# what it is. Side by side, each half is too narrow to read in a column
# of text.
strip = f"pad=iw:ih+{STRIP}:0:{STRIP}:color={GROUND}"
graph = (
    f"[0:v]fps={FPS},scale={WIDTH}:-2:flags=lanczos,{strip},"
    f"drawtext=fontfile={FONT}:text='{LABEL}':fontcolor={MUTED}:fontsize={LABEL_SIZE}:x=16:y=10[term];"
    f"[1:v]scale={WIDTH}:-2:flags=lanczos,{strip},{','.join(labels)}[tab];"
    f"[term][tab]vstack=inputs=2,pad=iw:ih+12:0:0:color={GROUND},format=yuv420p[out]"
)
subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-i", OUT + "-term.gif",
                "-framerate", str(FPS), "-i", FRAMES + "/f%05d.png",
                "-filter_complex", graph, "-map", "[out]", "-c:v", "libx264", "-crf", "23",
                "-preset", "slow", "-movflags", "+faststart", OUT + ".mp4"], check=True)

# An animated WebP of the same, for places that show images and not
# videos.
subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-i", OUT + ".mp4",
                "-vf", "fps=3,scale=640:-2:flags=lanczos", "-c:v", "libwebp", "-lossless", "0",
                "-q:v", "14", "-loop", "0", "-an", OUT + ".webp"], check=True)
