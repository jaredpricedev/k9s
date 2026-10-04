#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright Authors of K9s
"""Rasterize exported simulation-screen cells without adding UI content.

Requires Pillow. Filenames and JSON metadata distinguish true-color output
from the xterm-256 palette approximation. Uses fixed cell dimensions and font
paths so repeated renders with the same Pillow/font versions are deterministic.
"""

import argparse
import json
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont


def rasterize(path, font_directory):
    fixture = json.loads(path.read_text())
    cell_width, cell_height, padding = 10, 22, 16
    regular = ImageFont.truetype(str(font_directory / "DejaVuSansMono.ttf"), 16)
    bold = ImageFont.truetype(str(font_directory / "DejaVuSansMono-Bold.ttf"), 16)
    width, height = fixture["Width"], fixture["Height"]
    image = Image.new("RGB", (width * cell_width + padding * 2,
                             height * cell_height + padding * 2), "#0b0e11")
    draw = ImageDraw.Draw(image)
    for y, row in enumerate(fixture["Cells"]):
        for x, cell in enumerate(row):
            left, top = padding + x * cell_width, padding + y * cell_height
            draw.rectangle((left, top, left + cell_width - 1, top + cell_height - 1),
                           fill=f"#{cell['Bg']:06x}")
            draw.text((left, top), cell["Text"], font=bold if cell["Bold"] else regular,
                      fill=f"#{cell['Fg']:06x}")
    image.save(path.with_suffix(".png"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--font-directory", type=Path,
                        default=Path("/usr/share/fonts/truetype/dejavu"))
    args = parser.parse_args()
    for path in sorted(args.directory.glob("pods-*.json")):
        rasterize(path, args.font_directory)


if __name__ == "__main__":
    main()
