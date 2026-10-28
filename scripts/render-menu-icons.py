#!/usr/bin/env python3
"""Render Cherry's home menu icons into data/menuicons/ from Tabler Icons (MIT).

Usage (from the repo root):
  python scripts/render-menu-icons.py <tabler package dir> [--magick PATH]

<tabler package dir> is the extracted @tabler/icons npm package (the folder holding package.json,
LICENSE and icons/outline). Each icon is a coloured circle badge on a 128x108 canvas (the size of the
client's menu placeholder homenew_menu_icon_error.png) with a white outline glyph on top.

Writes <name>.png per ICONS entry, LICENSE-tabler-icons.txt and README.md (the link -> icon table is read
from data/menu.json "icons"). Needs Pillow and ImageMagick (SVG through its librsvg delegate).
"""

import argparse
import json
import pathlib
import subprocess
import sys
import tempfile

from PIL import Image, ImageChops, ImageDraw, ImageFilter

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "data" / "menuicons"
MENU = ROOT / "data" / "menu.json"

SS = 4  # supersampling factor, downscaled with LANCZOS at the end
W, H = 128, 108  # final canvas
R = 38  # badge radius (diameter 76, the client placeholder disc is about 74)
GLYPH = 42  # glyph box

# File stem -> (Tabler glyph name, badge colour). Colours are per category.
ICONS = {
    "home": ("home", "#FF7FA8"),
    "brush": ("brush", "#FF7FA8"),
    "dice-5": ("dice-5", "#FF7FA8"),
    "confetti": ("confetti", "#FF7FA8"),
    "building-cottage": ("building-cottage", "#FF7FA8"),
    "armchair-2": ("armchair-2", "#FF7FA8"),
    "photo": ("photo", "#FF7FA8"),
    "shirt": ("shirt", "#A77BFF"),
    "hanger": ("hanger", "#A77BFF"),
    "checklist": ("checklist", "#A77BFF"),
    "star": ("star", "#A77BFF"),
    "mood-smile": ("mood-smile", "#A77BFF"),
    "medal": ("medal", "#A77BFF"),
    "palette": ("palette", "#A77BFF"),
    "shopping-bag": ("shopping-bag", "#FF9F43"),
    "tag": ("tag", "#FF9F43"),
    "diamond": ("diamond", "#FF9F43"),
    "heart": ("heart", "#FF9F43"),
    "credit-card": ("credit-card", "#FF9F43"),
    "building-store": ("building-store", "#FF9F43"),
    "hammer": ("hammer", "#FF9F43"),
    "arrows-exchange": ("arrows-exchange", "#FF9F43"),
    "books": ("books", "#FF9F43"),
    "box": ("box", "#FF9F43"),
    "gift": ("gift", "#F2545B"),
    "book-2": ("book-2", "#F2545B"),
    "rotate-clockwise-2": ("rotate-clockwise-2", "#F2545B"),
    "users": ("users", "#4DB8FF"),
    "user-plus": ("user-plus", "#4DB8FF"),
    "mail-forward": ("mail-forward", "#4DB8FF"),
    "messages": ("messages", "#4DB8FF"),
    "notebook": ("notebook", "#4DB8FF"),
    "user-edit": ("user-edit", "#4DB8FF"),
    "circles": ("circles", "#4DB8FF"),
    "thumb-up": ("thumb-up", "#4DB8FF"),
    "mailbox": ("mailbox", "#4DB8FF"),
    "user-star": ("user-star", "#4DB8FF"),
    "settings": ("settings", "#8393A7"),
    "bell": ("bell", "#8393A7"),
    "ad": ("ad", "#8393A7"),
    "bulb": ("bulb", "#8393A7"),
    "layout-grid": ("layout-grid", "#8393A7"),
    "map-2": ("map-2", "#2BBFA4"),
    "fish": ("fish", "#2BBFA4"),
    "trophy": ("trophy", "#2BBFA4"),
    "flag-3": ("flag-3", "#2BBFA4"),
    "dice": ("dice", "#2BBFA4"),
    "plant-2": ("plant-2", "#6CC650"),
    "trees": ("trees", "#6CC650"),
    "dice-4": ("dice-4", "#6CC650"),
    "pencil": ("pencil", "#6CC650"),
    "cloud-upload": ("cloud-upload", "#6CC650"),
    "sparkles": ("sparkles", "#6CC650"),
    "building-factory-2": ("building-factory-2", "#6CC650"),
    "backpack": ("backpack", "#6CC650"),
    "paw": ("paw", "#B98B65"),
    "bookmark": ("bookmark", "#B98B65"),
    "egg": ("egg", "#B98B65"),
    "calendar-event": ("calendar-event", "#6D7CFF"),
    "clover": ("clover", "#6D7CFF"),
    "world": ("world", "#6D7CFF"),
    "code": ("code", "#6D7CFF"),
    "device-gamepad-2": ("device-gamepad-2", "#6D7CFF"),
    "device-gamepad": ("device-gamepad", "#6D7CFF"),
    "clipboard-text": ("clipboard-text", "#6D7CFF"),
    "barrel": ("barrel", "#6D7CFF"),
    "player-play": ("player-play", "#6D7CFF"),
    "book": ("book", "#6D7CFF"),
    "hourglass": ("hourglass", "#6D7CFF"),
    "wand": ("wand", "#6D7CFF"),
    "crown": ("crown", "#E9B824"),
    "ticket": ("ticket", "#E9B824"),
    "package": ("package", "#E9B824"),
    "calendar-check": ("calendar-check", "#E9B824"),
    "list-check": ("list-check", "#E9B824"),
    "coins": ("coins", "#E9B824"),
    "droplet": ("droplet", "#FF9F43"),
    "color-swatch": ("color-swatch", "#FF7FA8"),
}


def glyph_alpha(tabler_dir, glyph, magick, px):
    """Rasterize one outline glyph in white and return its alpha channel at px x px."""
    svg = (tabler_dir / "icons" / "outline" / f"{glyph}.svg").read_text(encoding="utf-8")
    svg = svg.replace("currentColor", "#ffffff")
    with tempfile.TemporaryDirectory() as tmp:
        src = pathlib.Path(tmp, "glyph.svg")
        dst = pathlib.Path(tmp, "glyph.png")
        src.write_text(svg, encoding="utf-8")
        subprocess.run(
            [magick, "-background", "none", "-density", "2000", str(src), "-resize", f"{px}x{px}", str(dst)],
            check=True,
        )
        return Image.open(dst).convert("RGBA").getchannel("A")


def badge(colour, alpha):
    """A coloured circle with a soft shadow, a darker rim, a gloss and the white glyph."""
    big = (W * SS, H * SS)
    cx, cy, r = W * SS // 2, H * SS // 2, R * SS
    rgb = tuple(int(colour[i : i + 2], 16) for i in (1, 3, 5))
    dark = tuple(int(c * 0.8) for c in rgb)

    img = Image.new("RGBA", big, (0, 0, 0, 0))
    shadow = Image.new("RGBA", big, (0, 0, 0, 0))
    ImageDraw.Draw(shadow).ellipse((cx - r, cy - r + 6 * SS, cx + r, cy + r + 6 * SS), fill=(40, 30, 60, 90))
    img.alpha_composite(shadow.filter(ImageFilter.GaussianBlur(3 * SS)))

    draw = ImageDraw.Draw(img)
    draw.ellipse((cx - r, cy - r, cx + r, cy + r), fill=dark + (255,))
    inner = r - 4 * SS
    draw.ellipse((cx - inner, cy - inner, cx + inner, cy + inner), fill=rgb + (255,))

    disc = Image.new("L", big, 0)
    ImageDraw.Draw(disc).ellipse((cx - inner, cy - inner, cx + inner, cy + inner), fill=255)
    gloss = Image.new("L", big, 0)
    ImageDraw.Draw(gloss).ellipse((cx - inner * 0.75, cy - inner * 0.95, cx + inner * 0.75, cy + inner * 0.05), fill=70)
    gloss_layer = Image.new("RGBA", big, (255, 255, 255, 0))
    gloss_layer.putalpha(ImageChops.multiply(gloss, disc))
    img.alpha_composite(gloss_layer)

    glyph = Image.new("RGBA", alpha.size, (255, 255, 255, 255))
    glyph.putalpha(alpha)
    img.alpha_composite(glyph, dest=(cx - alpha.size[0] // 2, cy - alpha.size[1] // 2))
    return img.resize((W, H), Image.LANCZOS)


def readme(version, links):
    """README.md: source, licence, and the file -> glyph -> link types table (from data/menu.json)."""
    by_file = {}
    for link, file in links.items():
        by_file.setdefault(file, []).append(link)
    lines = [
        "# Home menu icons",
        "",
        "Cherry's own pictures for the LINE PLAY home MENU grid, served at `/cherry/menu/<file>` and",
        "named by `icons` in `data/menu.json`. They are not game assets.",
        "",
        f"- Source: `@tabler/icons` {version} (npm), outline style, stroke 2, from `icons/outline/`.",
        "- Licence: MIT, Copyright (c) 2020-2026 Paweł Kuna; the text is in `LICENSE-tabler-icons.txt`.",
        "- Rendering: `scripts/render-menu-icons.py` makes each file a 128x108 RGBA PNG: a coloured circle",
        "  badge per category and a white glyph (the client placeholder is 128x108).",
        "",
        "| File | Glyph | Badge | Link types |",
        "| --- | --- | --- | --- |",
    ]
    for file in sorted(by_file):
        stem = file[: -len(".png")]
        glyph, colour = ICONS[stem]
        types = ", ".join(f"`{link}`" for link in by_file[file])
        lines.append(f"| `{file}` | `{glyph}` | `{colour}` | {types} |")
    return "\n".join(lines) + "\n"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("tabler_dir", type=pathlib.Path)
    ap.add_argument("--magick", default="magick", help="ImageMagick 7 executable")
    args = ap.parse_args()

    tabler = args.tabler_dir
    version = json.loads((tabler / "package.json").read_text(encoding="utf-8"))["version"]
    menu = json.loads(MENU.read_text(encoding="utf-8"))
    links = menu.get("icons", {})
    unknown = sorted(set(links.values()) - {f"{stem}.png" for stem in ICONS})
    if unknown:
        sys.exit(f"data/menu.json icons names files this script does not render: {unknown}")

    OUT.mkdir(parents=True, exist_ok=True)
    for stem, (glyph, colour) in ICONS.items():
        alpha = glyph_alpha(tabler, glyph, args.magick, GLYPH * SS)
        badge(colour, alpha).save(OUT / f"{stem}.png", optimize=True)
    (OUT / "LICENSE-tabler-icons.txt").write_bytes((tabler / "LICENSE").read_bytes())
    (OUT / "README.md").write_text(readme(version, links), encoding="utf-8", newline="\n")
    print(f"rendered {len(ICONS)} icons from @tabler/icons {version} into {OUT}")


if __name__ == "__main__":
    main()
