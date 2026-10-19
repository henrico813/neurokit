"""Tests for answering a request, run the way neurokit serve runs it.

Each test starts ``python -m neurokit`` with a pipe at file 3, so it sees
what the broker sees: the exit status, the answer on file 3 and the log on
stderr.
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
    """Writes a tool whose set-name command runs body, and answers it.

    The tool records, while it imports, whether file 3 is inheritable.

    Args:
        tmp_path: A folder for the tool module.
        body: The source of the set_name function's body.

    Returns:
        The finished process and the text written to file 3.
    """
    tool = """
        import os

        from neurokit import Done, Fail, Tool

        inheritable_at_import = os.get_inheritable(3)
        tool = Tool("t", broker_url_env="T_URL", max_handlers=1)

        @tool.command(deadline_ms=1, max_body_bytes=1, max_response_bytes=1)
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
        """A Fail without write sends none, so the broker says change-unknown.

        Callers retry a change only after not-sent, so a library that filled
        one in could repeat a change.
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
            pytest.param(
                "return {'names': {name}}",
                "TypeError: Object of type set",
                id="result-not-json",
            ),
        ],
    )
    def test_crash_leaves_answer_empty(self, tmp_path, body, error):
        """A crash writes no answer, so the broker reports it as a crash.

        The traceback, which can hold a token, stays in the broker's log.
        Half an answer, or a Done without write, must not reach the caller
        as a result.
        """
        process, written = handle_request(tmp_path, body)

        assert (process.returncode, written) == (1, "")
        assert error in process.stderr

    def test_started_programs_cannot_inherit_answer_pipe(self, tmp_path):
        """A program the tool starts, even at import, cannot inherit file 3.

        After the handler exits, the broker waits up to 200 ms for every copy
        of file 3 to close, so a program left holding one slows every reply.
        """
        body = "return inheritable_at_import"

        process, written = handle_request(tmp_path, body)

        assert json.loads(written) == {"result": False}
