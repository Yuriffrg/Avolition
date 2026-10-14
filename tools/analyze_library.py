"""Produce the same local inventory as Analyze Library without launching the UI."""
import argparse
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from owpreview.animation_analysis import analyze


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--catalog', type=Path, help='Optional existing Whitehound catalog; read only.')
    parser.add_argument('--toolchain', type=Path, help='Optional existing toolchain/active.json; not historical export provenance.')
    args = parser.parse_args()
    root, output = args.root.resolve(), args.output.resolve()
    if output.is_relative_to(root):
        parser.error('Write the report outside the analyzed export to keep its assets read-only.')
    annotations, installed, warnings = {}, None, []
    labels_available = False
    if args.catalog:
        try:
            annotations = json.loads(args.catalog.read_text(encoding='utf-8'))['annotations']
            if not isinstance(annotations, dict):
                raise ValueError('Annotations must be an object.')
            labels_available = True
        except (OSError, ValueError, KeyError) as error:
            warnings.append('User-label coverage unavailable: '+str(error))
    if args.toolchain:
        installed = json.loads(args.toolchain.read_text(encoding='utf-8')).get('package')
    result = analyze(root, annotations=annotations, installed_toolchain=installed, labels_available=labels_available)
    result['limitations'].extend(warnings)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf-8')
    print(json.dumps({'output': str(output), 'assets': result['assetCount'], 'occurrences': result['occurrenceCount'],
                      'coverage': result['coverage'], 'parseErrors': len(result['parseErrors'])}, indent=2))


if __name__ == '__main__':
    main()
