"""Runs a real neurokit serve for a tool's own tests."""

import contextlib
import json
import os
import subprocess
import tempfile
import time
from pathlib import Path


@contextlib.contextmanager
def serve_broker(manifest, env=None):
    """Runs ``neurokit serve`` on a free local port and yields its URL.

    Each broker gets its own port, so test runs at the same time never
    reach each other's broker. It uses the ``neurokit`` binary on PATH and
    serves the manifest file unchanged. A test can first edit the
    manifest's ``program`` field to run a fake handler. serve and its
    handlers run in the current folder, so a handler can import the tool
    from there. Set the manifest's ``broker_url_env`` variable to the URL
    before calling ``neurokit.call``.

    Example::

        with serve_broker("manifest.json") as url:
            monkeypatch.setenv("GREETER_URL", url)

    Args:
        manifest: The path of the manifest to serve.
        env: The environment for serve and its handlers, as for
            ``subprocess.Popen``; None passes this process's own.

    Yields:
        The broker's URL, such as ``http://127.0.0.1:41234``.

    Raises:
        RuntimeError: serve stopped, or did not listen within 10 s. The
            message holds serve's log, such as a failed start-up check.
    """
    with tempfile.TemporaryDirectory() as work:
        log_path = Path(work) / "serve.log"
        with log_path.open("w") as log:
            serve = subprocess.Popen(
                [
                    "neurokit",
                    "serve",
                    "--listen",
                    "127.0.0.1:0",
                    os.fspath(manifest),
                ],
                stdout=log,
                stderr=log,
                env=env,
            )
        try:
            yield "http://" + wait_for_address(serve, log_path)
        finally:
            serve.terminate()
            try:
                serve.wait(timeout=10)
            except subprocess.TimeoutExpired:
                serve.kill()
                serve.wait()


def wait_for_address(serve, log_path):
    """Returns the host and port that serve logged once it listened.

    Args:
        serve: The running ``neurokit serve`` process.
        log_path: The file that holds serve's stdout and stderr.

    Returns:
        The address from the ``serve_start`` log entry, such as
        ``127.0.0.1:41234``.

    Raises:
        RuntimeError: serve stopped, or logged no address within 10 s.
    """
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        for line in log_path.read_text().splitlines(keepends=True):
            # serve may still be writing the last line.
            if line.endswith("\n") and '"msg":"serve_start"' in line:
                return json.loads(line)["addr"]
        if serve.poll() is not None:
            raise RuntimeError(
                f"neurokit serve stopped: {log_path.read_text()}"
            )
        time.sleep(0.05)
    raise RuntimeError(f"neurokit serve did not start: {log_path.read_text()}")
