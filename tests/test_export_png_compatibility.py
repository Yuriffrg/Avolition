"""Re-export into pre-Go folders without modifying earlier texture files."""
import io
from pathlib import Path
import tempfile
import unittest

from PIL import Image, PngImagePlugin
from owpreview.export_bundle import export_bundle


class ExportPNGCompatibilityTests(unittest.TestCase):
    def test_existing_texture_reuse_and_conflict_protection(self):
        for mode, change in [('L', None), ('I;16', None), ('RGBA', None),
                             ('L', 'pixel'), ('RGBA', 'hidden-rgb'),
                             ('I;16', 'precision'), ('L', 'metadata'), ('L', 'corrupt')]:
            with self.subTest(mode=mode, change=change), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                image = Image.new(mode, (16, 16))
                old = io.BytesIO()
                image.save(old, format='PNG', compress_level=0)
                target = root / 'tex' / 'shared.png'
                target.parent.mkdir()
                target.write_bytes(old.getvalue())
                timestamp = target.stat().st_mtime_ns
                if change in ('pixel', 'precision'):
                    image.putpixel((0, 0), 1)
                elif change == 'hidden-rgb':
                    image.putpixel((0, 0), (1, 0, 0, 0))
                info = PngImagePlugin.PngInfo()
                if change == 'metadata':
                    info.add(b'gAMA', (45455).to_bytes(4, 'big'))
                fresh = io.BytesIO()
                image.save(fresh, format='PNG', compress_level=9, pnginfo=info)
                content = fresh.getvalue()
                if change == 'corrupt':
                    content = content[:45]  # Truncated IDAT.
                self.assertNotEqual(content, old.getvalue())

                def build(scenes, textures):
                    scenes['c4d'].write_bytes(b'Native scene using tex/shared.png')
                    textures.mkdir()
                    (textures / 'shared.png').write_bytes(content)

                def export():
                    return export_bundle(root / 'source.owmap', root, 'Map.c4d', build, ('c4d',))

                if change:
                    with self.assertRaises(FileExistsError):
                        export()
                    self.assertFalse((root / 'Map.c4d').exists())
                else:
                    result = export()
                    self.assertEqual(result['textureCount'], 1)
                    self.assertTrue(Path(result['c4dPath']).is_file())
                    self.assertTrue(export()['reused'])
                self.assertEqual(target.read_bytes(), old.getvalue())
                self.assertEqual(target.stat().st_mtime_ns, timestamp)
                self.assertFalse(list(root.glob('.whitehound-export-*')))
