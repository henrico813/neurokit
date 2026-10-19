"""What a command returns or raises, and how one request is answered."""

import json
import os
import sys
from dataclasses import KW_ONLY, dataclass

UNKNOWN, NOT_SENT, SENT, VERIFIED = "unknown", "not-sent", "sent", "verified"


@dataclass(frozen=True)
class Done:
    """A mutating command's result, with how far its change got.

    ``write`` has no default: a forgotten outcome is a crash, which the
    broker reports as change-unknown rather than a guess.

    Example::

        return Done({"id": "w1"}, write=VERIFIED)
    """

    result: object
    _: KW_ONLY
    write: str


class Fail(Exception):  # noqa: N818
    """A known failure, which the caller gets as a code and a message.

    On the caller's side, ``request_id`` finds the request in the broker's
    log.

    Args:
        code: A shared code such as ``invalid-request``, or one the
            command lists in ``errors``.
        message: Plain words for the caller; never a secret.
        write: For a mutation, how far the change got. Without it, the
            broker reports change-unknown.
    """

    def __init__(self, code: str, message: str, *, write: str | None = None):
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message
        self.write = write
        self.request_id: str | None = None


def answer_request(tool):
    """Runs the command serve asked for and writes its answer to file 3.

    serve passes the arguments on stdin, the command's name in
    ``NEUROKIT_COMMAND`` and the answer pipe as file 3. Exits 0 after a
    result and 1 after a Fail. Any other exception exits 1 with nothing
    written, so the broker reports a crash and the traceback stays in its
    log.
    """
    function = tool.commands[os.environ["NEUROKIT_COMMAND"]].function
    status = 0
    try:
        value = function(**json.load(sys.stdin))
        if isinstance(value, Done):
            body = {"result": value.result, "write": value.write}
        else:
            body = {"result": value}
    except Fail as failure:
        status = 1
        body = {"error": {"code": failure.code, "message": failure.message}}
        if failure.write is not None:
            body["write"] = failure.write
    # Built before the pipe opens, so a result that is not JSON writes
    # nothing, not half an answer.
    text = json.dumps(body)
    with os.fdopen(3, "w") as answer:
        answer.write(text)
    sys.exit(status)
