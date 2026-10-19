"""Tests for the handler shim, run the way neurokit serve runs it.

Each test starts ``python -m neurokit_handler`` in a subprocess with a pipe
at file 3, so it sees exactly what the broker sees: the exit status, the
answer on file 3, and the log on stderr.
"""

import json
import os
import subprocess
import sys
import textwrap
from pathlib import Path

import pytest

SHIM_DIR = Path(__file__).resolve().parents[1]


def run_shim(tool_dir, tool_source, *shim_args, stdin="{}"):
    """Writes tool.py into tool_dir and runs the shim on it as serve does.

    Args:
        tool_dir: A folder for the tool module.
        tool_source: The source of the tool module, ``tool``.
        *shim_args: The shim's arguments, such as ``("tool",)``.
        stdin: The command's arguments, as JSON.

    Returns:
        The finished process and the text written to file 3.
    """
    (tool_dir / "tool.py").write_text(textwrap.dedent(tool_source))
    read_end, write_end = os.pipe()
    env = {
        **os.environ,
        "PYTHONPATH": str(SHIM_DIR),
        "NEUROKIT_COMMAND": "set-name",
    }
    process = subprocess.run(
        [sys.executable, "-m", "neurokit_handler", *shim_args],
        cwd=tool_dir,
        env=env,
        input=stdin,
        capture_output=True,
        text=True,
        timeout=30,
        # serve passes the answer pipe as file 3, so put it there.
        preexec_fn=lambda: os.dup2(write_end, 3),
        close_fds=False,
    )
    os.close(write_end)
    with os.fdopen(read_end) as answer:
        return process, answer.read()


class TestAnswer:
    """What the shim writes to file 3 for each way a function can end."""

    @pytest.mark.parametrize(
        ("body", "status", "answer"),
        [
            pytest.param(
                "return Done({'name': name}, write=VERIFIED)",
                0,
                {"result": {"name": "Ann"}, "write": "verified"},
                id="done-verified",
            ),
            pytest.param(
                "raise Fail('provider-rejected', 'no', write=SENT)",
                1,
                {
                    "error": {"code": "provider-rejected", "message": "no"},
                    "write": "sent",
                },
                id="failure-sent",
            ),
            pytest.param(
                "return {'name': name}",
                0,
                {"result": {"name": "Ann"}},
                id="plain-result-without-write",
            ),
            pytest.param(
                "raise Fail('provider-rejected', 'no')",
                1,
                {"error": {"code": "provider-rejected", "message": "no"}},
                id="failure-without-write",
            ),
        ],
    )
    def test_answer_matches_what_function_did(
        self, tmp_path, body, status, answer
    ):
        """The answer carries the function's write, and never one it left out.

        A second copy of Fail, as when ``python -m`` loads a module twice,
        would turn every known failure into a crash. A default write would
        bring back the fantasy sidecar's bug, where a sent change read as
        not-sent.
        """
        tool = f"""
            from neurokit_handler import SENT, VERIFIED, Done, Fail

            def set_name(name):
                {body}
        """

        process, written = run_shim(
            tmp_path, tool, "tool", stdin='{"name": "Ann"}'
        )

        assert (process.returncode, json.loads(written)) == (status, answer)

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
        """A crash writes no answer, so its traceback stays in the log.

        A traceback can hold a token or raw provider output. A Done without
        write is a crash too, so a forgotten outcome reaches the caller as
        change-unknown, never as a guess.
        """
        tool = f"""
            from neurokit_handler import Done

            def set_name(name):
                {body}
        """

        process, written = run_shim(
            tmp_path, tool, "tool", stdin='{"name": "A"}'
        )

        assert (process.returncode, written) == (1, "")
        assert error in process.stderr

    def test_tool_children_cannot_inherit_answer_pipe(self, tmp_path):
        """A child process the tool starts cannot keep the answer pipe open.

        A child that keeps file 3 open holds up its request until the
        broker's 200 ms drain limit; in the prototype, before that limit, a
        daemon held a request for 5 s after its handler exited.
        """
        tool = """
            import os

            def set_name(name):
                return {"inherited": os.get_inheritable(3)}
        """

        process, written = run_shim(
            tmp_path, tool, "tool", stdin='{"name": "A"}'
        )

        assert (process.returncode, json.loads(written)) == (
            0,
            {"result": {"inherited": False}},
        )


class TestCheck:
    """The start-up check, which serve runs once before it listens."""

    def test_check_mode_reports_tool_check_failure(self, tmp_path):
        """The check calls the tool's check(), whose reason stops serve.

        Without it, a missing secret would show up only on the first
        request, not as serve's one-line reason to stop.
        """
        tool = """
            def set_name(name):
                pass

            def check():
                raise RuntimeError("NAMES_TOKEN is required")
        """

        process, _ = run_shim(tmp_path, tool, "--check", "tool")

        assert process.returncode == 1
        assert (
            process.stderr.splitlines()[-1]
            == "RuntimeError: NAMES_TOKEN is required"
        )
