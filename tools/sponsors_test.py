#!/usr/bin/env python3
"""Tests for tools/sponsors.py. Run: python3 tools/sponsors_test.py"""
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).parent))
import sponsors  # noqa: E402


def node(login, dollars, name=None, privacy="PUBLIC", once=False):
    return {"privacyLevel": privacy, "tier": {"monthlyPriceInDollars": dollars, "isOneTime": once},
            "sponsorEntity": {"login": login, "name": name}}


class Sponsors(unittest.TestCase):
    def test_who_is_listed(self):
        got = sponsors.public_sponsors([
            node("small", 5), node("backer", 10, "A Backer"), node("city", 50, "Stadt Musterhausen"),
            node("shy", 100, privacy="PRIVATE"), node("once", 500, once=True),
            node("bad login/../x", 100), {"privacyLevel": "PUBLIC", "tier": None, "sponsorEntity": None},
        ])
        self.assertEqual(got, [("city", "Stadt Musterhausen", 50), ("backer", "A Backer", 10)])

    def test_render_escapes_names(self):
        text = sponsors.render([("evil", '"><script>alert(1)</script>', 25), ("b", "B & Co", 10)])
        self.assertNotIn("<script>", text)
        self.assertIn("B &amp; Co", text)
        self.assertIn('href="https://github.com/evil"', text)
        self.assertIn("No public sponsors yet", sponsors.render([]))

    def test_rewrite_only_between_the_markers(self):
        text = "a\n%s\nold\n%s\nb\n" % (sponsors.START, sponsors.END)
        self.assertEqual(sponsors.rewrite(text, "new"), "a\n%s\nnew\n%s\nb\n" % (sponsors.START, sponsors.END))
        self.assertEqual(sponsors.rewrite(sponsors.rewrite(text, "new"), "new"), sponsors.rewrite(text, "new"))
        with self.assertRaises(SystemExit):
            sponsors.rewrite("no markers", "new")

    def test_readme_has_the_markers(self):
        text = sponsors.README.read_text()
        self.assertEqual(sponsors.rewrite(text, "x").count("x\n" + sponsors.END), 1)


if __name__ == "__main__":
    unittest.main()
