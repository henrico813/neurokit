"""Answers the ask command the way a real tool's handler would.

It imports a large library, prints a log line and leaves a child process
running, as git can leave ssh running. serve runs it once per request.
"""

import json
import os
import subprocess
import sys

import google.genai  # noqa: F401


def main():
    """Read the question from stdin and write the answer to the answer pipe.

    serve passes the answer pipe as file 3, the one after stdin, stdout and
    stderr, so ``os.fdopen(3)`` opens it. Anything printed goes to serve's
    log instead, so it cannot spoil the answer.
    """
    question = json.load(sys.stdin)["question"]
    print(f"answering {question!r}")
    subprocess.Popen(["sleep", "30"])
    with os.fdopen(3, "w") as answer:
        json.dump({"result": {"answer": f"you asked {question}"}}, answer)


if __name__ == "__main__":
    main()
