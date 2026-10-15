package textures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avolition/gocore/internal/oracle"
)

// generator writes many images with Pillow (and hand-built PNG/DDS/BMP files
// for layouts Pillow cannot save) and stores Pillow's
// Image.open(path).convert('RGBA') pixels next to each one.
const generator = `
import os, struct, zlib
import numpy as np
from PIL import Image
out = args[0]
rng = np.random.default_rng(1234)
manifest = []

def pattern(w, h, c, maxv=255):
    yy, xx = np.mgrid[0:h, 0:w]
    base = (xx * 7 + yy * 13)[..., None] + np.arange(c) * 50
    noise = rng.integers(0, maxv + 1, (h, w, c))
    pick = rng.random((h, w, 1)) < 0.5
    return np.where(pick, noise, base % (maxv + 1))

def record(name):
    path = os.path.join(out, name)
    try:
        with Image.open(path) as im:
            rgba = im.convert('RGBA')
            with open(path + '.rgba', 'wb') as f:
                f.write(rgba.tobytes())
            manifest.append([name, rgba.width, rgba.height, ''])
    except Exception as e:
        manifest.append([name, 0, 0, '%s: %s' % (type(e).__name__, e)])

def save(im, name, **kw):
    im.save(os.path.join(out, name), **kw)
    record(name)

def chunk(kind, data):
    return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data) & 0xffffffff)

def raw_png(name, w, h, depth, ctype, plte=None, trns=None, interlace=0, values=None):
    channels = {0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[ctype]
    row_bytes = (w * depth * channels + 7) // 8
    rows = []
    for y in range(h):
        if values is not None:
            row = bytes(rng.choice(values, row_bytes).astype(np.uint8))
        else:
            row = bytes(rng.integers(0, 256, row_bytes, dtype=np.uint8))
        if depth == 16 and ctype == 0:
            row = bytes(b if i % 2 or rng.random() < 0.3 else 0 for i, b in enumerate(row))
        rows.append(b'\x00' + row)
    raw = b''.join(rows)
    if interlace:
        # Re-encode through Pillow is not possible for every layout; skip.
        pass
    data = b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', w, h, depth, ctype, 0, 0, 0))
    if plte is not None:
        data += chunk(b'PLTE', plte)
    if trns is not None:
        data += chunk(b'tRNS', trns)
    data += chunk(b'IDAT', zlib.compress(raw)) + chunk(b'IEND', b'')
    with open(os.path.join(out, name), 'wb') as f:
        f.write(data)
    record(name)

sizes = [(1, 1), (3, 5), (17, 13), (64, 48), (129, 67)]
for w, h in sizes:
    tag = '%dx%d' % (w, h)
    rgba = Image.fromarray(pattern(w, h, 4).astype(np.uint8), 'RGBA')
    rgb = rgba.convert('RGB')
    gray = rgba.getchannel(0)
    la = Image.merge('LA', (gray, rgba.getchannel(3)))
    pal = Image.fromarray((pattern(w, h, 1)[..., 0] % 200).astype(np.uint8), 'L').convert('P')
    pal.putpalette(list(rng.integers(0, 256, 200 * 3)))
    i16 = Image.fromarray((pattern(w, h, 1, 700)[..., 0]).astype(np.uint16), 'I;16')
    bw = gray.convert('1')
    # PNG
    for mode, im in (('rgba', rgba), ('rgb', rgb), ('l', gray), ('la', la), ('p', pal), ('i16', i16), ('1', bw)):
        save(im, 'png-%s-%s.png' % (mode, tag))
    save(pal, 'png-ptrns-%s.png' % tag, transparency=bytes(rng.integers(0, 256, 150, dtype=np.uint8)))
    save(pal, 'png-psingle-%s.png' % tag, transparency=3)
    save(gray, 'png-ltrns-%s.png' % tag, transparency=int(np.asarray(gray).flat[0]))
    save(rgb, 'png-rgbtrns-%s.png' % tag, transparency=tuple(int(v) for v in np.asarray(rgb)[0, 0]))
    save(rgba, 'png-interlaced-%s.png' % tag, optimize=False)
    raw_png('raw-g2trns-%s.png' % tag, w, h, 2, 0, trns=struct.pack('>H', 1))
    raw_png('raw-g4trns0-%s.png' % tag, w, h, 4, 0, trns=struct.pack('>H', 0))
    raw_png('raw-g1trns-%s.png' % tag, w, h, 1, 0, trns=struct.pack('>H', 1))
    raw_png('raw-g8trns-%s.png' % tag, w, h, 8, 0, trns=struct.pack('>H', 0x0107), values=[7, 9, 200])
    raw_png('raw-g16-%s.png' % tag, w, h, 16, 0)
    raw_png('raw-g16trns-%s.png' % tag, w, h, 16, 0, trns=struct.pack('>H', 5), values=[0, 5, 6])
    raw_png('raw-rgb16-%s.png' % tag, w, h, 16, 2)
    raw_png('raw-rgb16trns-%s.png' % tag, w, h, 16, 2, trns=struct.pack('>HHH', 0x0107, 0x0207, 0x0307), values=[7, 1, 2, 3])
    raw_png('raw-la16-%s.png' % tag, w, h, 16, 4)
    raw_png('raw-rgba16-%s.png' % tag, w, h, 16, 6)
    raw_png('raw-p4short-%s.png' % tag, w, h, 4, 3, plte=bytes(range(30)), trns=b'\x10\x80')
    raw_png('raw-p8short-%s.png' % tag, w, h, 8, 3, plte=bytes(rng.integers(0, 256, 60, dtype=np.uint8)))
    # JPEG
    for sub in (0, 1, 2):
        save(rgb, 'jpeg-rgb-s%d-%s.jpg' % (sub, tag), quality=90, subsampling=sub)
    save(rgb, 'jpeg-progressive-%s.jpg' % tag, quality=75, progressive=True)
    save(gray, 'jpeg-l-%s.jpg' % tag, quality=90)
    save(rgba.convert('CMYK'), 'jpeg-cmyk-%s.jpg' % tag, quality=90)
    # TGA
    for mode, im in (('rgba', rgba), ('rgb', rgb), ('l', gray), ('la', la), ('p', pal)):
        save(im, 'tga-%s-%s.tga' % (mode, tag))
        save(im, 'tga-rle-%s-%s.tga' % (mode, tag), compression='tga_rle')
        save(im, 'tga-top-%s-%s.tga' % (mode, tag), orientation=1)
    # TIFF
    for compression in (None, 'tiff_lzw', 'tiff_adobe_deflate', 'packbits'):
        for mode, im in (('rgba', rgba), ('rgb', rgb), ('l', gray), ('p', pal), ('1', bw), ('i16', i16)):
            save(im, 'tiff-%s-%s-%s.tif' % (mode, compression, tag), compression=compression)
    # BMP
    for mode, im in (('rgb', rgb), ('rgba', rgba), ('l', gray), ('p', pal), ('1', bw)):
        save(im, 'bmp-%s-%s.bmp' % (mode, tag))
    # WebP
    save(rgb, 'webp-lossless-rgb-%s.webp' % tag, lossless=True)
    save(rgba, 'webp-lossless-rgba-%s.webp' % tag, lossless=True)
    save(rgb, 'webp-lossy-rgb-%s.webp' % tag, quality=80)
    save(rgba, 'webp-lossy-rgba-%s.webp' % tag, quality=70)

def dds(name, w, h, flags, fourcc=b'\0\0\0\0', bits=0, masks=(0, 0, 0, 0), dxgi=None, body=None, block=16, extra=b''):
    pf = struct.pack('<II4sI4I', 32, flags, fourcc, bits, *masks)
    header = struct.pack('<4sI', b'DDS ', 124) + struct.pack('<6I', 0x1007, h, w, 0, 0, 1) + b'\0' * 44 + pf + struct.pack('<5I', 0x1000, 0, 0, 0, 0)
    if dxgi is not None:
        header += struct.pack('<5I', dxgi, 3, 0, 1, 0)
    if body is None:
        body = bytes(rng.integers(0, 256, ((w + 3) // 4) * ((h + 3) // 4) * block, dtype=np.uint8))
    with open(os.path.join(out, name), 'wb') as f:
        f.write(header + extra + body)
    record(name)

for w, h in ((4, 4), (5, 7), (16, 8), (33, 17)):
    tag = '%dx%d' % (w, h)
    for cc, block in ((b'DXT1', 8), (b'DXT3', 16), (b'DXT5', 16), (b'ATI1', 8), (b'BC4U', 8), (b'ATI2', 16), (b'BC5U', 16), (b'BC5S', 16)):
        dds('dds-%s-%s.dds' % (cc.decode(), tag), w, h, 0x4, cc, block=block)
    for dxgi, block in ((71, 8), (74, 16), (77, 16), (80, 8), (83, 16), (84, 16), (95, 16), (96, 16), (98, 16), (99, 16)):
        for repeat in range(3):
            dds('dds-dx10-%d-%d-%s.dds' % (dxgi, repeat, tag), w, h, 0x4, b'DX10', dxgi=dxgi, block=block)
    pixels = lambda size: bytes(rng.integers(0, 256, w * h * size, dtype=np.uint8))
    dds('dds-dx10-rgba-%s.dds' % tag, w, h, 0x4, b'DX10', dxgi=28, body=pixels(4))
    dds('dds-rgb565-%s.dds' % tag, w, h, 0x40, bits=16, masks=(0xF800, 0x7E0, 0x1F, 0), body=pixels(2))
    dds('dds-argb4-%s.dds' % tag, w, h, 0x41, bits=16, masks=(0xF00, 0xF0, 0xF, 0xF000), body=pixels(2))
    dds('dds-bgr24-%s.dds' % tag, w, h, 0x40, bits=24, masks=(0xFF0000, 0xFF00, 0xFF, 0), body=pixels(3))
    dds('dds-abgr32-%s.dds' % tag, w, h, 0x41, bits=32, masks=(0xFF, 0xFF00, 0xFF0000, 0xFF000000), body=pixels(4))
    dds('dds-l8-%s.dds' % tag, w, h, 0x20000, bits=8, body=pixels(1))
    dds('dds-la8-%s.dds' % tag, w, h, 0x20001, bits=16, body=pixels(2))
    dds('dds-p8-%s.dds' % tag, w, h, 0x20, bits=8, extra=pixels(0) + bytes(rng.integers(0, 256, 1024, dtype=np.uint8)), body=pixels(1))

# Hand-made BMPs: RLE8, RLE4, 16-bit bitfields, top-down 32-bit.
def bmp(name, w, h, bits, compression, body, palette=b'', header_extra=b'', height=None, colors=0):
    info_size = 40
    offset = 14 + info_size + len(header_extra) + len(palette)
    info = struct.pack('<IiiHHIIiiII', info_size, w, h if height is None else height, 1, bits, compression, len(body), 2835, 2835, colors, 0)
    data = b'BM' + struct.pack('<IHHI', offset + len(body), 0, 0, offset) + info + header_extra + palette + body
    with open(os.path.join(out, name), 'wb') as f:
        f.write(data)
    record(name)

palette = bytes(rng.integers(0, 256, 256 * 4, dtype=np.uint8))
rle = b''
for y in range(6):
    rle += bytes([3, 7, 0, 3, 1, 2, 3, 0, 2, 9, 0, 0])
rle += bytes([0, 1])
bmp('bmp-rle8.bmp', 10, 6, 8, 1, rle, palette)
rle4 = b''
for y in range(5):
    rle4 += bytes([4, 0x3c, 0, 4, 0x12, 0x34, 0, 0])
rle4 += bytes([0, 1])
bmp('bmp-rle4.bmp', 8, 5, 4, 2, rle4, palette[:64])
w, h = 7, 5
row = (w * 2 + 3) & ~3
bmp('bmp-565.bmp', w, h, 16, 3, bytes(rng.integers(0, 256, row * h, dtype=np.uint8)), header_extra=struct.pack('<3I', 0xF800, 0x7E0, 0x1F))
bmp('bmp-555.bmp', w, h, 16, 0, bytes(rng.integers(0, 256, row * h, dtype=np.uint8)))
bmp('bmp-topdown32.bmp', w, h, 32, 0, bytes(rng.integers(0, 256, w * 4 * h, dtype=np.uint8)), height=-h)
result = manifest
`

func TestDecodeMatchesPillow(t *testing.T) {
	folder := t.TempDir()
	var manifest [][4]any
	if err := json.Unmarshal(oracle.Exec(t, generator, folder).MustValue(t), &manifest); err != nil {
		t.Fatal(err)
	}
	maxDelta := map[string]int{}
	compared := 0
	for _, entry := range manifest {
		name, pyError := entry[0].(string), entry[3].(string)
		data, err := os.ReadFile(filepath.Join(folder, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeImage(data, nil)
		if pyError != "" {
			if err == nil {
				t.Errorf("%s: Pillow failed (%s) but Go decoded", name, pyError)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		want, _ := os.ReadFile(filepath.Join(folder, name+".rgba"))
		if got.Width != int(entry[1].(float64)) || got.Height != int(entry[2].(float64)) || len(got.Pix) != len(want) {
			t.Errorf("%s: size %dx%d, Pillow %vx%v", name, got.Width, got.Height, entry[1], entry[2])
			continue
		}
		compared++
		kind := strings.SplitN(name, "-", 2)[0]
		if strings.HasPrefix(name, "jpeg-") {
			kind = strings.Join(strings.SplitN(name, "-", 3)[:2], "-")
		}
		delta, first := 0, -1
		for i := range want {
			d := int(want[i]) - int(got.Pix[i])
			if d < 0 {
				d = -d
			}
			if d > 0 && first < 0 {
				first = i
			}
			delta = max(delta, d)
		}
		maxDelta[kind] = max(maxDelta[kind], delta)
		tolerance := 0
		if strings.HasPrefix(kind, "jpeg") {
			tolerance = jpegTolerance
		}
		if delta > tolerance {
			p := first / 4
			t.Errorf("%s: max delta %d at pixel %d (x=%d y=%d) channel %d: Go %v, Pillow %v", name, delta, p, p%got.Width, p/got.Width,
				first%4, got.Pix[p*4:p*4+4], want[p*4:p*4+4])
		}
	}
	t.Logf("compared %d images; max |Go-Pillow| per format: %v", compared, maxDelta)
}

// image/jpeg's IDCT differs from libjpeg-turbo's jidctint (observed max 3).
const jpegTolerance = 4
