"""Calls a broker command from Python by running the neurokit binary."""

import json
import os
import subprocess

from neurokit.answer import UNKNOWN, Done, Fail


def call(manifest: str | os.PathLike[str], command: str, /, **args) -> object:
    """Runs one command and returns what its function returned.

    The ``neurokit`` binary on PATH checks the arguments against the
    manifest and sends them to the broker. A mutation returns ``Done``.
    An argument passed None is left out, so an optional one takes its
    default and a required one is refused as missing.

    Example::

        call("/opt/scout/manifest.json", "analyze", question=q, context=c)

    Raises:
        Fail: The command, the broker or the binary replied with an error,
            or the binary stopped without a readable reply.
        FileNotFoundError: No ``neurokit`` binary is on PATH.
    """
    # The broker refuses null, so None can only mean "not given".
    sent = {name: value for name, value in args.items() if value is not None}
    finished = subprocess.run(
        ["neurokit", "call", os.fspath(manifest), command, json.dumps(sent)],
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
    )
    try:
        reply = json.loads(finished.stdout)
    except json.JSONDecodeError as decode_error:
        raise describe_missing_reply(
            manifest, command, finished.returncode
        ) from decode_error
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


def describe_missing_reply(manifest, command, exit_status):
    """Returns the Fail for when ``neurokit call`` printed no readable reply.

    The binary may have sent the request before it stopped, so a mutating
    command gets change-unknown, as the binary gives for a lost reply.

    Args:
        manifest: The manifest that call was given.
        command: The command that call ran.
        exit_status: The binary's exit status, such as -9 after a kill.

    Returns:
        broker-unavailable for a command that changes nothing, or
        change-unknown with write unknown for a mutating command.
    """
    with open(manifest, encoding="utf-8") as file:
        commands = json.load(file)["commands"]
    message = f"neurokit call exited {exit_status} with no readable reply"
    if commands[command].get("mutating"):
        return Fail("change-unknown", message, write=UNKNOWN)
    return Fail("broker-unavailable", message)
