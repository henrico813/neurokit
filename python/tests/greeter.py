"""A small tool that the tests run under a real neurokit serve."""

import os
from pathlib import Path
from typing import Annotated

from pydantic import Field

from neurokit import NOT_SENT, VERIFIED, Broker, Done, Fail

broker = Broker("greeter", broker_url_env="GREETER_URL", max_handlers=1)


@broker.check
def check():
    """Fails when the token file is missing, like a real tool whose secret
    is missing.
    """
    (Path(os.environ["GREETER_DIR"]) / "token").read_text()


@broker.command(
    deadline_ms=5_000, max_body_bytes=1_000, max_response_bytes=1_000
)
def greet(
    name: Annotated[str, Field(max_length=20)],
    title: Annotated[str, Field(max_length=10)] | None = None,
) -> dict:
    """Returns a greeting for name, with title when one is given."""
    if title is None:
        return {"greeting": f"hello {name}"}
    return {"greeting": f"hello {title} {name}"}


@broker.command(
    mutating=True,
    lock="write",
    approval=True,
    errors=["empty-greeting"],
    deadline_ms=5_000,
    max_body_bytes=1_000,
    max_response_bytes=1_000,
)
def save_greeting(text: Annotated[str, Field(max_length=20)]) -> Done:
    """Saves text and reads it back to verify it; empty text fails with
    not-sent.
    """
    if not text:
        raise Fail("empty-greeting", "say something", write=NOT_SENT)
    path = Path(os.environ["GREETER_DIR"]) / "greeting"
    path.write_text(text)
    return Done({"saved": path.read_text()}, write=VERIFIED)
