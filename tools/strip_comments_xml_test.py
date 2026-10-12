#!/usr/bin/env python3
# Tests for strip-comments-xml.py. Run: python3 tools/strip_comments_xml_test.py

import importlib.util
import os
import sys
import unittest

sys.dont_write_bytecode = True
_here = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location(
    "strip_comments_xml", os.path.join(_here, "strip-comments-xml.py"))
sx = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sx)


class StripTest(unittest.TestCase):
    def test_whole_line_comments_go_with_their_lines(self):
        src = (
            '<?xml version="1.0" encoding="utf-8"?>\n'
            "<a>\n"
            "    <!-- one -->\n"
            "    <b/>\n"
            "    <!--\n"
            "      two\n"
            "    -->\n"
            "</a>\n"
        )
        want = '<?xml version="1.0" encoding="utf-8"?>\n<a>\n    <b/>\n</a>\n'
        self.assertEqual(sx.strip(src), want)

    def test_inline_comment_goes_alone(self):
        src = '<a x="1"/> <!-- c -->\n<b/><!-- d --><c/>\n'
        self.assertEqual(sx.strip(src), '<a x="1"/> \n<b/><c/>\n')

    def test_comment_at_file_start_and_end(self):
        src = "<!--\n  head\n-->\n<a/>\n<!-- tail -->"
        self.assertEqual(sx.strip(src), "<a/>\n")

    def test_idempotent(self):
        src = "<a>\n  <!-- c -->\n  <b/>\n</a>\n"
        once = sx.strip(src)
        self.assertEqual(sx.strip(once), once)


if __name__ == "__main__":
    unittest.main()
