"""Answers the ask command the way a real tool's handler would.

It imports a large library, prints a log line and leaves a child process
running, as git can leave ssh running. serve runs it once per request.
"""

import subprocess

import google.genai  # noqa: F401


def ask(question):
    """Returns an answer that names the question.

    Args:
        question: The question from the request.
    """
    print(f"answering {question!r}")
    subprocess.Popen(["sleep", "30"])
    return {"answer": f"you asked {question}"}


def check():
    """Passes once the module, with google-genai, has imported."""
