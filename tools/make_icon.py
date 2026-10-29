import math
from PIL import Image, ImageDraw

S = 1024
BLUE_TOP = (64, 140, 255)
BLUE_BOT = (26, 74, 205)
WHITE = (255, 255, 255, 255)
DARK = (20, 62, 180, 255)


def rounded_bg(size=S, radius_ratio=0.225):
    grad = Image.new("RGB", (1, size))
    for y in range(size):
        t = (y / (size - 1)) ** 0.9
        grad.putpixel((0, y), tuple(int(BLUE_TOP[i] + (BLUE_BOT[i] - BLUE_TOP[i]) * t) for i in range(3)))
    grad = grad.resize((size, size))

    mask = Image.new("L", (size, size), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, size - 1, size - 1], radius=int(size * radius_ratio), fill=255)
    out = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    out.paste(grad, (0, 0), mask)
    return out


def blade(draw, cx, cy, r_in, r_out, a0, a1, curve=0.16):
    """一片叶子：内弧 r_in、外弧 r_out，外缘带一点弧度。"""
    steps = 24
    outer = []
    for j in range(steps + 1):
        t = j / steps
        a = a0 + (a1 - a0) * t
        rr = r_out * (1.0 - curve * (1.0 - math.sin(math.pi * t)))
        outer.append((cx + rr * math.cos(a), cy + rr * math.sin(a)))
    inner = []
    for j in range(steps, -1, -1):
        t = j / steps
        a = a0 + (a1 - a0) * t
        rr = r_in + (r_out - r_in) * 0.10 * math.sin(math.pi * t)
        inner.append((cx + rr * math.cos(a), cy + rr * math.sin(a)))
    draw.polygon(outer + inner, fill=WHITE)


def icon_fan():
    img = rounded_bg()
    d = ImageDraw.Draw(img)
    cx = cy = S / 2
    r_in = S * 0.09
    r_out = S * 0.345
    gap = 14  # 度，叶间留缝
    for k in range(4):
        a0 = math.radians(38 + k * 90 + gap / 2)
        a1 = math.radians(38 + k * 90 + 72 - gap / 2)
        blade(d, cx, cy, r_in, r_out, a0, a1)
    d.ellipse([cx - r_in, cy - r_in, cx + r_in, cy + r_in], fill=WHITE)
    d.ellipse([cx - r_in * 0.44, cy - r_in * 0.44, cx + r_in * 0.44, cy + r_in * 0.44], fill=DARK)
    return img


def preview_sheet(img, path):
    sizes = [16, 24, 32, 48, 64, 128, 256]
    pad = 20
    w = sum(s for s in sizes) + pad * (len(sizes) + 1)
    h = 256 + pad * 2
    sheet = Image.new("RGBA", (w, h), (20, 22, 28, 255))
    x = pad
    for s in sizes:
        sheet.alpha_composite(img.resize((s, s), Image.LANCZOS), (x, pad + (256 - s) // 2))
        x += s + pad
    sheet.save(path)


if __name__ == "__main__":
    img = icon_fan()
    img.save("icon-1024.png")
    img.resize((256, 256), Image.LANCZOS).save("icon-256.png")
    preview_sheet(img, "icon-sizes.png")
    img.save("icon.ico", sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)])
    print("ok")
