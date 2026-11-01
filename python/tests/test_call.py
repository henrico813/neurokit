"""Tests for call and serve_broker, mostly against the greeter test tool."""

import os
import subprocess
import sys
from pathlib import Path

import pytest

import neurokit
from neurokit.testing import serve_broker

TESTS_DIR = Path(__file__).resolve().parent


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
        # The handler imports neurokit from python/ and greeter from tests/.
        env.setenv("PYTHONPATH", f"{TESTS_DIR.parent}:{TESTS_DIR}")
        env.setenv("GREETER_DIR", str(work))
        subprocess.run(
            [sys.executable, "-m", "neurokit", "manifest", "greeter", path],
            cwd=TESTS_DIR,
            check=True,
        )
        with serve_broker(path) as url:
            env.setenv("GREETER_URL", url)
            yield path


class TestServeBroker:
    """Starting a real broker for a tool's own tests."""

    def test_raises_with_reason_when_serve_stops(
        self, greeter_manifest, tmp_path, monkeypatch
    ):
        """serve_broker raises with serve's reason if the broker cannot start.

        A start-up check can fail when a tool's tests start its real broker,
        such as when a secret is missing. Without the reason, the author sees
        only a timeout or a refused connection.
        """
        # Point the test tool's start-up check at an empty folder, so its
        # secret is missing.
        monkeypatch.setenv("GREETER_DIR", str(tmp_path))

        with (
            pytest.raises(RuntimeError) as raised,
            serve_broker(greeter_manifest),
        ):
            pass

        assert "check failed: FileNotFoundError" in str(raised.value)


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

    def test_leaves_out_none_arguments(self, greeter_manifest):
        """An argument passed None is not sent, so it counts as not given.

        Fantasy passes ``drop_player=None`` when nothing is dropped. Sent
        as null, the binary refuses it; a required argument passed None
        must still be refused, as missing.
        """
        result = neurokit.call(
            greeter_manifest, "greet", name="Ann", title=None
        )
        with pytest.raises(neurokit.Fail) as raised:
            neurokit.call(greeter_manifest, "greet", name=None)

        assert result == {"greeting": "hello Ann"}
        assert raised.value.message == "name is required"

    @pytest.mark.parametrize(
        ("command", "code", "write"),
        [
            pytest.param("greet", "broker-unavailable", None, id="read"),
            pytest.param(
                "save-greeting", "change-unknown", "unknown", id="mutation"
            ),
        ],
    )
    def test_raises_fail_when_binary_is_killed(
        self, greeter_manifest, tmp_path, monkeypatch, command, code, write
    ):
        """call raises Fail when the binary is killed before it replies.

        A caller that handles only Fail would crash on a JSON error. A
        killed mutation may already have made its change, so it must never
        read as broker-unavailable, which callers retry.
        """
        binary = tmp_path / "neurokit"
        binary.write_text("#!/bin/sh\nkill -9 $$\n")
        binary.chmod(0o755)
        monkeypatch.setenv("PATH", f"{tmp_path}:{os.environ['PATH']}")

        with pytest.raises(neurokit.Fail) as raised:
            neurokit.call(greeter_manifest, command)

        assert (raised.value.code, raised.value.write) == (code, write)
