#!/usr/bin/env python3
"""One-shot helper to build the per-OS release zips for a mod repo (fs/shell):
zip = entry binary (renamed to the bare `entry` name from manifest.json,
`.exe` on windows) + manifest.json, matching what mods_install expects to
find once extracted under mods/<id>/files/.

ponytail: throwaway script for this release, not a repo convention — delete
once the release is cut, or keep if more releases follow the same shape.
"""
import shutil
import sys
import zipfile
from pathlib import Path

# (os_key, bin_suffix triple, is_windows)
TARGETS = [
    ("windows", "x86_64-pc-windows-msvc.exe", True),
    ("linux", "x86_64-unknown-linux-gnu", False),
    ("macos", "aarch64-apple-darwin", False),
]


def main():
    binary_name = sys.argv[1]  # e.g. barrakuda-mcp-fs
    root = Path(__file__).parent
    out_dir = root / "release"
    out_dir.mkdir(exist_ok=True)

    for os_key, suffix, is_windows in TARGETS:
        src = root / "bin" / f"{binary_name}-{suffix}"
        if not src.exists():
            print(f"skip {os_key}: {src} not found")
            continue

        entry_name = f"{binary_name}.exe" if is_windows else binary_name
        zip_path = out_dir / f"{binary_name}-{os_key}.zip"

        with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as zf:
            info = zipfile.ZipInfo(entry_name)
            if not is_windows:
                info.external_attr = 0o755 << 16  # preserve the exec bit for unix
            with open(src, "rb") as f:
                zf.writestr(info, f.read())
            zf.write(root / "manifest.json", "manifest.json")

        print(f"wrote {zip_path}")


if __name__ == "__main__":
    main()
