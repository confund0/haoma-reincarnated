#!/usr/bin/env python3
# Tests for strip-comments-kt.py. Run: python3 tools/strip_comments_kt_test.py

import importlib.util
import os
import sys
import unittest

sys.dont_write_bytecode = True
_here = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location(
    "strip_comments_kt", os.path.join(_here, "strip-comments-kt.py"))
skt = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(skt)


class StripTest(unittest.TestCase):
    def test_line_and_block_comments_go(self):
        src = "val a = 1 // note\n/* block */val b = 2\n"
        self.assertEqual(skt.strip(src), "val a = 1 \nval b = 2\n")

    def test_comment_markers_in_strings_stay(self):
        src = 'val u = "http://x" // c\nval c = \'/\'\n'
        self.assertEqual(skt.strip(src), 'val u = "http://x" \nval c = \'/\'\n')

    def test_backtick_name_with_apostrophe(self):
        # An apostrophe in a backtick name must not open a char literal;
        # the comments after it must still go.
        src = (
            "class T {\n"
            "    // first\n"
            "    @Test fun `the wire's frame is kept`() {\n"
            "        val x = 1 // second\n"
            "    }\n"
            "    /* third */\n"
            "}\n"
        )
        want = (
            "class T {\n"
            "    \n"
            "    @Test fun `the wire's frame is kept`() {\n"
            "        val x = 1 \n"
            "    }\n"
            "    \n"
            "}\n"
        )
        self.assertEqual(skt.strip(src), want)

    def test_backtick_name_with_slashes_and_quote(self):
        src = 'fun `a // b "c`() = 1 // gone\n'
        self.assertEqual(skt.strip(src), 'fun `a // b "c`() = 1 \n')

    def test_license_header_kept(self):
        src = "// SPDX-License-Identifier: MIT\npackage p // x\n"
        self.assertEqual(skt.strip(src), "// SPDX-License-Identifier: MIT\npackage p \n")

    def test_idempotent(self):
        src = "fun `it's`() {} // c\n"
        once = skt.strip(src)
        self.assertEqual(skt.strip(once), once)

    def test_haoma_directive_kept(self):
        src = "val a = 1\n//haoma:direction in\nval b = 2\n"
        self.assertEqual(skt.strip(src), src)

    def test_haoma_with_space_stripped(self):
        src = "val a = 1\n// haoma: x\nval b = 2\n"
        self.assertEqual(skt.strip(src), "val a = 1\n\nval b = 2\n")

    def test_haoma_in_string_untouched(self):
        src = 'val u = "//haoma:direction in"\n'
        self.assertEqual(skt.strip(src), src)

    def test_haoma_in_block_comment_stripped(self):
        src = "val a = 1 /*haoma:direction in*/\nval b = 2\n"
        self.assertEqual(skt.strip(src), "val a = 1 \nval b = 2\n")

    def test_haoma_directive_group_keeps_only_directive(self):
        src = "// some commentary\n//haoma:direction in\nval a = 1\n"
        self.assertEqual(skt.strip(src), "\n//haoma:direction in\nval a = 1\n")

    def test_haoma_directive_idempotent(self):
        src = "val a = 1\n//haoma:direction in\nval b = 2\n"
        once = skt.strip(src)
        self.assertEqual(skt.strip(once), once)


if __name__ == "__main__":
    unittest.main()
