"""Answers one broker request, or runs the start-up check, for a tool.

serve runs this once per request, with the arguments on stdin and the
answer pipe as file 3.
"""

import importlib
import json
import os
import sys

from neurokit_handler import Done, Fail


def main(argv):
    """Answers the requested command, or with ``--check`` runs the check.

    Args:
        argv: ``[module]`` or ``["--check", module]``.
    """
    if argv[0] == "--check":
        run_tool_check(argv[1])
    else:
        answer_request(argv[0])


def answer_request(module_name):
    """Calls the command's function and writes its answer to file 3.

    Any exception but Fail exits 1 with nothing written, so the broker sees
    a crash and the traceback goes only to its log.

    Args:
        module_name: The tool's handler module, such as ``scout.handler``.
    """
    # A child the tool starts must not hold the answer pipe open.
    os.set_inheritable(3, False)
    module = importlib.import_module(module_name)
    command = os.environ["NEUROKIT_COMMAND"].replace("-", "_")
    try:
        value, status = getattr(module, command)(**json.load(sys.stdin)), 0
        body = {"result": value}
        # A mutation that returns a plain value sends no write, so the
        # broker reports change-unknown.
        if isinstance(value, Done):
            body = {"result": value.result, "write": value.write}
    except Fail as failure:
        body, status = failure.body, 1
    text = json.dumps(body)
    with os.fdopen(3, "w") as answer:
        answer.write(text)
    sys.exit(status)


def run_tool_check(module_name):
    """Imports the tool's module and calls its ``check()``.

    A failing check's last stderr line becomes serve's reason to stop,
    such as ``RuntimeError: GEMINI_API_KEY is required``.

    Args:
        module_name: The tool's handler module, such as ``scout.handler``.
    """
    importlib.import_module(module_name).check()


if __name__ == "__main__":
    main(sys.argv[1:])
