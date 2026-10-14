# Avolition 0.8.12 third-party notices

Avolition application source is distributed under GPL-3.0-or-later. The full GPL version 3 text is in `LICENSE`. Third-party components retain their own licenses and copyright notices. The portable release includes its Electron runtime and frozen Python backend; retain their supplied notices when redistributing the application folder.

## Avolition brand artwork

The user supplied `Avolition.tif` as the application's brand artwork. Its existing artwork was converted and resized to `web/assets/avolition-logo.png` and `web/assets/avolition.ico`; it was not redesigned. Version 0.8.11 displayed it in the header and empty preview. Version 0.8.12 removes those workspace logos and retains the Windows application icon. The supplied artwork is distinct from the third-party runtime components described below. The application was previously named Whitehound; the existing **Whitehound C4D Bridge** companion folder, title and protocol remain unchanged for compatibility.

## Overwatch format and importer references

The binary formats, skeletal playback and material semantics are based on the supplied [OWLib / TankLib exporters](https://github.com/overtools/OWLib) and the [io_scene_owm Blender importer](https://github.com/overtools/io_scene_owm). The preview parser and evaluator are application source. The importer's `shader_metadata.py`, `BLMaterial.py` and `library.blend` informed texture role assignment and packed-channel decoding; [SOURCE_REFERENCE.md](owpreview/SOURCE_REFERENCE.md) records the source URLs and verified formulas. `owpreview/bone_names.py` contains the GPL-3.0-or-later `BoneUtil.py` name table from that importer, including the translation credit to Jupit (@Jupiiit). Binary FBX record conventions were checked against Blender's public FBX importer/exporter source; Avolition implements its own standalone writer and ships no Autodesk SDK. Blender, Cinema 4D and Octane are not bundled. Optional native C4D export sends scene data to the included companion running in the user's installed Cinema 4D with Octane; preview and FBX remain standalone.

- OWLib: MIT license; copyright (c) 2016-2024 overtools. The supplied notice is reproduced below.
- io_scene_owm: GPL version 3 or later, as declared by the supplied importer. See the application `LICENSE` for the license text and the upstream repository for authorship and source history.

Avolition can optionally download the released OWLib Toolchain from its official GitHub release. The complete upstream package, including its Third Party directory, remains under `toolchain`. DataTool runs as a separate process; the preview and C4D exporters retain the existing application implementation. The portable Avolition ZIP does not preinstall this optional download or include game assets.

The Maps workflow uses OWLib's `list-maps` and `extract-maps` commands. Its local `.owmap` reader and static scene adapter reference OWLib's map exporter and the io_scene_owm map importer; command and format provenance are recorded in [MAP_SOURCE_REFERENCE.md](owpreview/MAP_SOURCE_REFERENCE.md) and [MAP_FORMAT_REFERENCE.md](owpreview/MAP_FORMAT_REFERENCE.md). The application keeps repeated geometry shared, preserving placement transforms and material appearances. Static map export requires the bundled **Whitehound C4D Bridge 0.7.3**; bridge 0.7.2 remains compatible with animation exports. Editable object names are application metadata and do not rename upstream assets or joint identifiers.

Map support covers static geometry and common material maps, excluding recreation of map lights, sounds and effects. Automated checks use synthetic format/process fixtures and mocked native saves where indicated. They are not evidence of a live game extraction or native Cinema 4D/Octane rendering; see `VALIDATION.md` for the current verification record.

## Web renderer

Three.js 0.186.0 and its OrbitControls module are used under the MIT license. Their original notice is in `web/vendor/THREE-LICENSE.txt` in the source distribution and `resources/web/vendor/THREE-LICENSE.txt` in the portable application. The exact package version and npm integrity value are recorded beside it in `version.json`.

## Electron desktop runtime

[Electron](https://github.com/electron/electron) 44.4.3 is distributed under the MIT license. It bundles Chromium, Node.js, and other components with their own licenses. Electron's original license is retained as `LICENSE.electron` beside `Avolition.exe`, and its `LICENSES.chromium.html` is retained in that same application folder. The Chromium notice includes licenses for components incorporated into the runtime.

Do not substitute the application's GPL text for these component notices. The original Electron runtime files and notices accompany `Avolition.exe`. The packaged shell source is under `resources/app`.

## Python backend and build tools

The application bundles CPython and the required backend libraries under `resources/backend`. The backend is frozen with [PyInstaller](https://pyinstaller.org/), whose license includes its bootloader exception. Original CPython, PyInstaller, and collected dependency notices are retained in the application's `LICENSES` folder.

- [CPython](https://www.python.org/): Python Software Foundation license and its included third-party notices.
- [OpenSSL](https://www.openssl.org/): included by the CPython runtime; retain the runtime's OpenSSL notice and applicable license reference.
- PyInstaller: GPL with the project's bootloader exception; retain the original supplied notice.
- [Pillow](https://python-pillow.org/) **12.3.0**: MIT-CMU license, including the original PIL/Pillow copyright notices. The installed wheel's license also includes notices for its bundled image codecs and other libraries. The build copies this complete notice automatically into `LICENSES/pillow/`; retain that directory with the release. Pillow decodes exported images locally and does not modify them.
- NumPy **2.3.5**: BSD license and bundled component notices, retained in `LICENSES/numpy/`. Used for FBX transform calculations.
- Other collected Python dependencies, if present, retain their original installed-package license files and metadata.

Keep the complete `LICENSES` folder, `LICENSE.electron`, and `LICENSES.chromium.html` with redistributed portable copies.

This project includes no Overwatch game content. Overwatch, Blender, Cinema 4D, Microsoft, Electron, and other product names identify compatibility or dependencies and do not imply endorsement.

## OWLib MIT notice

MIT License

Copyright (c) 2016-2024 overtools

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
