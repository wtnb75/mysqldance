#!/usr/bin/env python3
"""Copy the newest deskil flow engine into .claude/scripts/flow.py.

Usage: sync_flow.py [CACHE_GLOB]
The source commit hash is recorded in .claude/scripts/flow.version.
"""
import glob
import shutil
import sys
from pathlib import Path

DEFAULT_GLOB = str(
    Path.home() / ".claude/plugins/cache/deskil/deskil/*/scripts/flow.py"
)
DEST_DIR = Path(__file__).resolve().parent


def main() -> int:
    pattern = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_GLOB
    sources = sorted(glob.glob(pattern), key=lambda p: Path(p).stat().st_mtime)
    if not sources:
        print(f"error: no flow.py matches {pattern}", file=sys.stderr)
        return 1
    src = Path(sources[-1])
    shutil.copyfile(src, DEST_DIR / "flow.py")
    version = src.parent.parent.name
    (DEST_DIR / "flow.version").write_text(version + "\n", encoding="utf-8")
    print(f"synced flow.py from {src} (version {version})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
