"""What a tool's handler module imports to answer the broker.

A tool's manifest runs it as ``python -m neurokit_handler <module>``, and
its start-up check as ``python -m neurokit_handler --check <module>``.
"""

UNKNOWN, NOT_SENT, SENT, VERIFIED = "unknown", "not-sent", "sent", "verified"


class Done:
    """A mutating command's result, with how far its change got.

    Args:
        result: The value the caller gets as ``result``.
        write: One of the write constants. It has no default, so a
            forgotten outcome is a crash, which the broker reports as
            change-unknown, never as a guess.
    """

    def __init__(self, result, *, write):
        self.result = result
        self.write = write


class Fail(Exception):
    """A known failure, which the caller gets as an error code and message.

    Args:
        code: A shared code, or one the command declares in the manifest.
        message: Plain words for the caller; never a secret or a traceback.
        write: For a mutating command, how far the change got. Without it,
            the broker reports change-unknown.
    """

    def __init__(self, code, message, write=None):
        super().__init__(message)
        self.body = {"error": {"code": code, "message": message}}
        if write is not None:
            self.body["write"] = write
