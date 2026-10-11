import unittest

from slugify import slugify


class SlugifyAcceptance(unittest.TestCase):
    def test_spaces_become_dashes(self):
        self.assertEqual("order-status-filter", slugify("  Order Status Filter "))

    def test_symbols_are_removed(self):
        self.assertEqual("v2-release", slugify("v2 release!"))


if __name__ == "__main__":
    unittest.main()
