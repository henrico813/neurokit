"""Tests for call, against a real neurokit serve running the greeter tool."""

import os
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

import pytest

import neurokit

TESTS_DIR = Path(__file__).resolve().parent
# serve always listens on port 8080.
BROKER_URL = "http://127.0.0.1:8080"


@pytest.fixture(scope="module")
def greeter_manifest(tmp_path_factory):
    """Runs serve, built from this repo, on a manifest written for greeter.

    Yields:
        The path of the manifest that serve runs.
    """
    work = tmp_path_factory.mktemp("greeter")
    path = work / "greeter.json"
    subprocess.run(
        ["go", "build", "-o", work / "neurokit", "./cmd/neurokit"],
        cwd=TESTS_DIR.parents[1],
        check=True,
    )
    (work / "token").write_text("secret")
    with pytest.MonkeyPatch.context() as env:
        # serve runs python3, which must be this one, with pydantic.
        python_bin = Path(sys.executable).parent
        env.setenv("PATH", f"{work}:{python_bin}:{os.environ['PATH']}")
        env.setenv("PYTHONPATH", str(TESTS_DIR.parent))
        env.setenv("GREETER_DIR", str(work))
        env.setenv("GREETER_URL", BROKER_URL)
        subprocess.run(
            [sys.executable, "-m", "neurokit", "manifest", "greeter", path],
            cwd=TESTS_DIR,
            check=True,
        )
        with open(work / "serve.log", "w") as log:
            serve = subprocess.Popen(
                ["neurokit", "serve", path],
                cwd=TESTS_DIR,
                stdout=log,
                stderr=log,
            )
            try:
                wait_until_ready(serve, work / "serve.log")
                yield path
            finally:
                serve.terminate()
                try:
                    serve.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    serve.kill()
                    serve.wait()


def wait_until_ready(serve, log_path):
    """Fails the test unless serve answers /readyz within about 10 s."""
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if serve.poll() is not None:
            pytest.fail(f"serve stopped: {log_path.read_text()}")
        try:
            urllib.request.urlopen(f"{BROKER_URL}/readyz", timeout=1)
            return
        except OSError:
            time.sleep(0.1)
    pytest.fail(f"serve did not get ready: {log_path.read_text()}")


class TestCall:
    """What call gives back for each way a command ends."""

    @pytest.mark.parametrize(
        ("command", "args", "expected"),
        [
            pytest.param(
                "greet",
                {"name": "Ann"},
                {"greeting": "hello Ann"},
                id="read",
            ),
            pytest.param(
                "save-greeting",
                {"text": "hi", "approval": "policy:test"},
                neurokit.Done({"saved": "hi"}, write=neurokit.VERIFIED),
                id="mutation",
            ),
        ],
    )
    def test_returns_what_function_returned(
        self, greeter_manifest, command, args, expected
    ):
        """call returns the function's value, and a mutation's write.

        This runs the whole path: the written manifest, the binary, serve
        and the answer pipe. A caller that lost the write could not tell a
        verified change from one that may not have happened.
        """
        result = neurokit.call(greeter_manifest, command, **args)

        assert result == expected

    def test_raises_function_fail_with_request_id(self, greeter_manifest):
        """call raises the function's Fail with its write and request ID.

        The write tells the caller whether a retry is safe, and the request
        ID finds the request in the broker's log.
        """
        with pytest.raises(neurokit.Fail) as raised:
            neurokit.call(greeter_manifest, "save-greeting", text="")

        failure = raised.value
        assert (failure.code, failure.write) == ("empty-greeting", "not-sent")
        assert failure.request_id.startswith("req-")
