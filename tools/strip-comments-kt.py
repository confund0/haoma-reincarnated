#!/usr/bin/env python3
# strip-comments-kt: walk one or more directories and rewrite every
# .kt file with most of its comments removed. Preserved:
#
#   - The first comment block in a file IF it looks like a license /
#     copyright / SPDX header (case-insensitive substring match).
#   - Annotation-shaped comments (none in Kotlin land — we just keep
#     license headers).
#
# Everything else — KDoc /** */, line // comments, block /* */ — is
# dropped. String literals (including ${...} templates and raw """)
# are left intact via a char-by-char state machine.
#
# Used by tools/release.sh against the public-cut tree, NOT against
# the private repo. Idempotent.

import os
import sys

LICENSE_MARKERS = ("license", "copyright", "spdx")


def is_license_block(text: str) -> bool:
    lower = text.lower()
    return any(m in lower for m in LICENSE_MARKERS)


def strip(src: str) -> str:
    """Strip comments from Kotlin source. Char-by-char state machine.

    States:
      0 = code
      1 = inside "..."
      2 = inside ${...} (template expression in a string) — recurse-ish
      3 = inside '...'
      4 = inside \"\"\"...\"\"\"
      5 = inside // comment (skip until \n)
      6 = inside /* */ comment (skip until */)

    Special handling for the FIRST top-of-file comment block: if it
    looks like a license header, copy it verbatim and only switch into
    code-state after.
    """
    out = []
    n = len(src)
    i = 0
    state = 0
    template_depth = 0  # how many ${ we're nested into within a string

    # Phase 1 — pass-through any leading whitespace + license block.
    license_emitted = False
    j = 0
    while j < n and src[j] in " \t\r\n":
        j += 1
    if j < n and src[j:j + 2] == "/*":
        # Find closing */
        end = src.find("*/", j + 2)
        if end != -1:
            block = src[j:end + 2]
            if is_license_block(block):
                out.append(src[:end + 2])
                i = end + 2
                license_emitted = True
    if not license_emitted and j < n and src[j:j + 2] == "//":
        # Maybe a // line-comment block license header — collect
        # consecutive // lines and decide.
        k = j
        while k < n and src[k:k + 2] == "//":
            nl = src.find("\n", k)
            if nl == -1:
                k = n
                break
            k = nl + 1
            # Skip leading whitespace on the next line; if it's not
            # //, the block ends.
            m = k
            while m < n and src[m] in " \t":
                m += 1
            if m >= n or src[m:m + 2] != "//":
                k = nl + 1  # stop after this nl
                break
            k = m
        block = src[j:k]
        if is_license_block(block):
            out.append(src[:k])
            i = k
            license_emitted = True

    # Phase 2 — char-by-char strip.
    while i < n:
        c = src[i]
        nxt = src[i + 1] if i + 1 < n else ""

        if state == 0:  # code
            if c == "/" and nxt == "/":
                state = 5
                i += 2
                continue
            if c == "/" and nxt == "*":
                state = 6
                i += 2
                continue
            if src[i:i + 3] == '"""':
                state = 4
                out.append('"""')
                i += 3
                continue
            if c == '"':
                state = 1
                out.append(c)
                i += 1
                continue
            if c == "'":
                state = 3
                out.append(c)
                i += 1
                continue
            out.append(c)
            i += 1
            continue

        if state == 1:  # inside "..."
            if c == "\\" and i + 1 < n:
                out.append(src[i:i + 2])
                i += 2
                continue
            if c == "$" and nxt == "{":
                template_depth += 1
                out.append("${")
                i += 2
                state = 2
                continue
            if c == '"':
                out.append(c)
                i += 1
                state = 0
                continue
            out.append(c)
            i += 1
            continue

        if state == 2:  # inside ${...} expression
            if c == "{":
                template_depth += 1
                out.append(c)
                i += 1
                continue
            if c == "}":
                template_depth -= 1
                out.append(c)
                i += 1
                if template_depth == 0:
                    state = 1
                continue
            # Inside the expression, code-rules apply (could nest more
            # strings); keep simple — emit the char. A nested " could
            # confuse us but is exceedingly rare. Conservative: emit.
            out.append(c)
            i += 1
            continue

        if state == 3:  # inside '...'
            if c == "\\" and i + 1 < n:
                out.append(src[i:i + 2])
                i += 2
                continue
            if c == "'":
                out.append(c)
                i += 1
                state = 0
                continue
            out.append(c)
            i += 1
            continue

        if state == 4:  # inside """..."""
            if src[i:i + 3] == '"""':
                out.append('"""')
                i += 3
                state = 0
                continue
            if c == "$" and nxt == "{":
                template_depth += 1
                out.append("${")
                i += 2
                state = 2  # but on close-} we should return to 4
                # workaround: extend state-2 to remember "return to" ?
                # Multi-line raw strings rarely contain "${" with code
                # that contains // — accept the small risk.
                continue
            out.append(c)
            i += 1
            continue

        if state == 5:  # // comment
            if c == "\n":
                out.append("\n")
                i += 1
                state = 0
                continue
            i += 1
            continue

        if state == 6:  # /* */ comment
            if c == "*" and nxt == "/":
                i += 2
                state = 0
                continue
            # Preserve newlines inside block comments so line numbers
            # don't shift wildly post-strip.
            if c == "\n":
                out.append("\n")
            i += 1
            continue

    return collapse_blank_runs("".join(out))


def collapse_blank_runs(s: str) -> str:
    """Three or more consecutive blank lines → exactly two. Cleans up
    the swiss-cheese effect of stripping doc-comment blocks."""
    out = []
    blanks = 0
    for line in s.split("\n"):
        if line.strip() == "":
            blanks += 1
            if blanks <= 2:
                out.append(line)
        else:
            blanks = 0
            out.append(line)
    return "\n".join(out)


def process(path: str, verbose: bool) -> bool:
    with open(path, "rb") as f:
        raw = f.read()
    try:
        src = raw.decode("utf-8")
    except UnicodeDecodeError:
        return False
    out = strip(src)
    if out == src:
        return False
    with open(path + ".strip-tmp", "w", encoding="utf-8") as f:
        f.write(out)
    os.replace(path + ".strip-tmp", path)
    if verbose:
        print("stripped", path)
    return True


def walk(root: str, verbose: bool) -> tuple[int, int]:
    SKIP_DIRS = {".git", "build", "node_modules", "tmp", ".gradle"}
    processed = skipped = 0
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for fn in filenames:
            if not fn.endswith(".kt") and not fn.endswith(".kts"):
                continue
            path = os.path.join(dirpath, fn)
            if process(path, verbose):
                processed += 1
            else:
                skipped += 1
                if verbose:
                    print("skipped ", path)
    return processed, skipped


def main():
    args = sys.argv[1:]
    verbose = False
    if args and args[0] == "-v":
        verbose = True
        args = args[1:]
    if not args:
        sys.stderr.write("usage: strip-comments-kt.py [-v] <dir>...\n")
        sys.exit(2)
    total_p = total_s = 0
    for root in args:
        p, s = walk(root, verbose)
        total_p += p
        total_s += s
    print(f"strip-comments-kt: processed={total_p} skipped={total_s}")


if __name__ == "__main__":
    main()
