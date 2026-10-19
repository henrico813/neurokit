"""Broker and its decorators, which say what goes into manifest.json."""

from collections.abc import Callable, Sequence
from dataclasses import dataclass


@dataclass(frozen=True)
class Command:
    """One command's function and its manifest settings."""

    function: Callable
    settings: dict


class Broker:
    """One broker's commands, collected by its decorators.

    Example::

        broker = Broker("scout", broker_url_env="SCOUT_URL", max_handlers=2)

    Args:
        name: The tool's name in the manifest.
        broker_url_env: The variable that holds the broker's URL on the
            caller's side.
        max_handlers: The most requests the broker runs at once.
    """

    def __init__(self, name: str, *, broker_url_env: str, max_handlers: int):
        self.name = name
        self.broker_url_env = broker_url_env
        self.max_handlers = max_handlers
        self.commands: dict[str, Command] = {}
        self.check_function: Callable | None = None

    def command[F: Callable](
        self,
        *,
        deadline_ms: int,
        max_body_bytes: int,
        max_response_bytes: int,
        mutating: bool = False,
        lock: str | None = None,
        approval: bool = False,
        errors: Sequence[str] = (),
    ) -> Callable[[F], F]:
        """Makes the function a command named after it, with ``_`` as ``-``.

        The arguments are the command's settings in the manifest, which the
        README's Terms describe.
        """

        def register(function: F) -> F:
            settings = {
                "deadline_ms": deadline_ms,
                "max_body_bytes": max_body_bytes,
                "max_response_bytes": max_response_bytes,
                "mutating": mutating,
                "lock": lock,
                "approval": approval,
                "errors": list(errors),
            }
            name = function.__name__.replace("_", "-")
            self.commands[name] = Command(function, settings)
            return function

        return register

    def check[F: Callable](self, function: F) -> F:
        """Makes the function the start-up check, such as reading a secret.

        serve runs it once before it listens. If it raises, serve stops with
        the exception's last line as its reason.
        """
        self.check_function = function
        return function
