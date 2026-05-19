"""Top-level Python fixture for Leonard's Python parser.

Exercises module-level functions, classes, and assignments. Keep this file
Python 3.4-compatible: gpython's parser does not support f-strings, walrus,
match statements, type aliases, or PEP 695 generic syntax.
"""


VERSION = "0.1"
PUBLIC_NAME = "leonard"
_internal = 42


def hello(name):
    return "hello " + name


def add(a, b, *rest, **opts):
    total = a + b
    for r in rest:
        total = total + r
    return total


def _helper():
    return None


class Widget:
    pass


class _PrivateWidget(Widget):
    pass
