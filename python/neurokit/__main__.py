"""Writes a tool's manifest, and runs its commands for serve.

The manifest tells serve to run ``handle`` once per request and
``check`` once at start.
"""

import importlib
import json
import os
import sys

from neurokit.answer import answer_request
from neurokit.manifest import build_manifest
from neurokit.tool import Tool

USAGE = """usage: python -m neurokit manifest MODULE PATH
       python -m neurokit handle MODULE
       python -m neurokit check MODULE"""


def main(argv):
    """Runs the action that argv names, as USAGE shows."""
    match argv:
        case ["manifest", module_name, path]:
            tool = find_tool(module_name)
            try:
                text = json.dumps(build_manifest(tool, module_name), indent=2)
            except ValueError as error:
                sys.exit(f"neurokit manifest: {error}")
            with open(path, "w") as out:
                out.write(text + "\n")
        case ["handle", module_name]:
            # A program the tool starts, even while it imports, must not
            # hold the answer pipe open, or the broker waits for it.
            os.set_inheritable(3, False)
            answer_request(find_tool(module_name))
        case ["check", module_name]:
            check_function = find_tool(module_name).check_function
            if check_function:
                check_function()
        case _:
            sys.exit(USAGE)


def find_tool(module_name):
    """Returns the one Tool the module defines, or exits naming the module."""
    module = importlib.import_module(module_name)
    tools = [
        value for value in vars(module).values() if isinstance(value, Tool)
    ]
    if len(tools) != 1:
        sys.exit(
            f"neurokit: {module_name} must define one Tool, not {len(tools)}"
        )
    return tools[0]


if __name__ == "__main__":
    main(sys.argv[1:])
