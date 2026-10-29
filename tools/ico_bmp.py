import struct
from PIL import Image

def bmp_entry(img):
    """把一个 RGBA 图做成 ICO 里的 DIB 条目（BMP 格式，非 PNG）。"""
    w, h = img.size
    px = img.convert("RGBA").load()
    # XOR 位图：BGRA，自下而上
    xor = bytearray()
    for y in range(h - 1, -1, -1):
        for x in range(w):
            r, g, b, a = px[x, y]
            xor += bytes((b, g, r, a))
    # AND 掩码：32bpp 用 alpha 就够，这里全 0（不透明）
    row = ((w + 31) // 32) * 4
    and_mask = bytes(row * h)
    hdr = struct.pack("<IiiHHIIiiII", 40, w, h * 2, 1, 32, 0, len(xor) + len(and_mask), 0, 0, 0, 0)
    return hdr + bytes(xor) + and_mask

def save_ico(img, sizes, path):
    blobs = []
    for s in sizes:
        blobs.append((s, bmp_entry(img.resize((s, s), Image.LANCZOS))))
    out = bytearray(struct.pack("<HHH", 0, 1, len(blobs)))
    offset = 6 + 16 * len(blobs)
    for s, data in blobs:
        w = 0 if s >= 256 else s
        out += struct.pack("<BBBBHHII", w, w, 0, 0, 1, 32, len(data), offset)
        offset += len(data)
    for _, data in blobs:
        out += data
    open(path, "wb").write(out)
    return len(out)

if __name__ == "__main__":
    src = Image.open("icon-1024.png")
    n = save_ico(src, [16, 20, 24, 32, 40, 48, 64, 128, 256], "icon.ico")
    print("icon.ico", n, "bytes")
