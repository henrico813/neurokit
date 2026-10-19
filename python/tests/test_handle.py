"""Tests for answering a request, run the way neurokit serve runs it.

Each test starts ``python -m neurokit handle`` with a pipe at file 3 and
sees what the broker sees: the exit status, the answer and stderr.
"""

import json
import os
import subprocess
import sys
import textwrap
from pathlib import Path

import pytest

PYTHON_DIR = Path(__file__).resolve().parents[1]


def handle_request(tmp_path, body):
    """Writes a tool whose set-name command runs body, and runs one request
    as serve would.

    The tool records, while it imports, whether file 3 is inheritable.

    Args:
        tmp_path: A folder for the tool module.
        body: The source of the set_name function's body.

    Returns:
        The finished process and the text written to file 3.
    """
    tool = """
        import os

        from neurokit import Broker, Done, Fail

        inheritable_at_import = os.get_inheritable(3)
        broker = Broker("t", broker_url_env="T_URL", max_handlers=1)

        @broker.command(deadline_ms=1, max_body_bytes=1, max_response_bytes=1)
        def set_name(name):
    """
    (tmp_path / "tool.py").write_text(
        textwrap.dedent(tool) + textwrap.indent(body, "    ")
    )
    read_end, write_end = os.pipe()
    with os.fdopen(read_end) as answer:
        try:
            process = subprocess.run(
                [sys.executable, "-m", "neurokit", "handle", "tool"],
                cwd=tmp_path,
                env={
                    **os.environ,
                    "PYTHONPATH": str(PYTHON_DIR),
                    "NEUROKIT_COMMAND": "set-name",
                },
                input='{"name": "Ann"}',
                capture_output=True,
                text=True,
                timeout=30,
                # serve passes the answer pipe as file 3, so put it there.
                preexec_fn=lambda: os.dup2(write_end, 3),
                close_fds=False,
            )
        finally:
            os.close(write_end)
        return process, answer.read()


class TestAnswer:
    """What a command's answer says for each way its function ends."""

    def test_fail_without_write_sends_no_write(self, tmp_path):
        """A Fail without write sends none, so for a mutating command the
        broker says change-unknown.

        Callers retry a change only after not-sent, so a library that filled
        in not-sent could make a caller repeat a change.
        """
        process, written = handle_request(
            tmp_path, "raise Fail('name-taken', 'no')"
        )

        assert (process.returncode, json.loads(written)) == (
            1,
            {"error": {"code": "name-taken", "message": "no"}},
        )

    @pytest.mark.parametrize(
        ("body", "error"),
        [
            pytest.param(
                "raise RuntimeError('token abc123 was refused')",
                "RuntimeError: token abc123",
                id="uncaught-exception",
            ),
            pytest.param(
                "return Done({'name': name})",
                "TypeError",
                id="done-without-write",
            ),
        ],
    )
    def test_crash_leaves_answer_empty(self, tmp_path, body, error):
        """A crash writes no answer, so nothing reaches the caller as a
        result, not even a Done without write.

        The traceback, which can hold a token, stays in the broker's log.
        """
        process, written = handle_request(tmp_path, body)

        assert (process.returncode, written) == (1, "")
        assert error in process.stderr

    def test_started_programs_cannot_inherit_answer_pipe(self, tmp_path):
        """A program the tool starts, even at import, cannot inherit file 3.

        A program that leaves the handler's process group and still holds
        file 3 makes the broker wait up to 200 ms before it replies.
        """
        body = "return inheritable_at_import"

        process, written = handle_request(tmp_path, body)

        assert json.loads(written) == {"result": False}
