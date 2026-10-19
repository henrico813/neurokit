"""Calls a broker command from Python by running the neurokit binary."""

import json
import os
import subprocess

from neurokit.answer import Done, Fail


def call(manifest: str | os.PathLike, command: str, /, **args) -> object:
    """Runs one command and returns what its function returned.

    The ``neurokit`` binary on PATH checks the arguments against the
    manifest and sends them to the broker. A mutation returns ``Done``.
    Every error reply, from the function, the broker or the binary, raises
    ``Fail``.

    Example::

        call("/opt/scout/manifest.json", "analyze", question=q, context=c)

    Raises:
        Fail: The command, the broker or the binary replied with an error.
        FileNotFoundError: No ``neurokit`` binary is on PATH.
    """
    finished = subprocess.run(
        ["neurokit", "call", os.fspath(manifest), command, json.dumps(args)],
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
    )
    reply = json.loads(finished.stdout)
    if "error" in reply:
        error = reply["error"]
        failure = Fail(
            error["code"], error["message"], write=reply.get("write")
        )
        failure.request_id = error.get("request_id")
        raise failure
    if "write" in reply:
        return Done(reply["result"], write=reply["write"])
    return reply["result"]
