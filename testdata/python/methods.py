"""Methods fixture: classes whose bodies contain methods.

The parser must emit one symbol for each class (kind=type) and one symbol per
method (kind=method) with qualified_name = ClassName.method_name.
"""


class Container:
    def __init__(self, name):
        self.name = name
        self.items = []

    def add(self, item):
        self.items.append(item)

    def remove(self, item):
        self.items.remove(item)

    def _refresh(self):
        return len(self.items)


class Renderer:
    def render(self, target):
        return str(target)

    def flush(self):
        return True
