#!/usr/bin/env python3
# strip-comments-xml: walk one or more directories and remove every
# <!-- --> comment from each .xml file. A comment that fills its lines
# alone is removed with those lines; an inline one only by itself.
# The files hold no CDATA, and "<" cannot occur unescaped in an
# attribute value, so a plain scan for "<!--" is safe.
#
# Used by tools/release.sh. Idempotent.

import os
import re
import sys

WHOLE_LINES = re.compile(r"^[ \t]*<!--.*?-->[ \t]*(?:\n|\Z)", re.S | re.M)
INLINE = re.compile(r"<!--.*?-->", re.S)


def strip(src: str) -> str:
    return INLINE.sub("", WHOLE_LINES.sub("", src))


def walk(root: str, verbose: bool) -> int:
    changed = 0
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in {"build", ".gradle"}]
        for fn in filenames:
            if not fn.endswith(".xml"):
                continue
            path = os.path.join(dirpath, fn)
            with open(path, encoding="utf-8") as f:
                src = f.read()
            out = strip(src)
            if out == src:
                continue
            with open(path + ".strip-tmp", "w", encoding="utf-8") as f:
                f.write(out)
            os.replace(path + ".strip-tmp", path)
            changed += 1
            if verbose:
                print("stripped", path)
    return changed


def main():
    args = sys.argv[1:]
    verbose = False
    if args and args[0] == "-v":
        verbose = True
        args = args[1:]
    if not args:
        sys.stderr.write("usage: strip-comments-xml.py [-v] <dir>...\n")
        sys.exit(2)
    total = sum(walk(root, verbose) for root in args)
    print(f"strip-comments-xml: stripped={total}")


if __name__ == "__main__":
    main()
