# Write a Python tool

This page builds `notes`, a small tool that saves and reads short notes, from start to finish. Follow it to write a new tool or move an old one onto neurokit. The README's [Python tools](../../README.md#python-tools) section lists every rule; this page shows them working together.

The broker is an HTTP server, `neurokit serve`, in the tool's container.

A tool has two halves. The broker side runs in the broker container, holds the tool's secrets and talks to the outside service. The caller side runs next to the assistant and holds no secrets.

## Install

You need Go and Python 3.12 or later. This installs the `neurokit` binary and the Python library:

```sh
nk=github.com/henrico813/neurokit
go install $nk/cmd/neurokit@v0.1.0
url=https://$nk/archive/v0.1.0.tar.gz
pip install "$url#subdirectory=python"
```

`go install` puts `neurokit` in Go's bin folder, `$(go env GOPATH)/bin` by default or `$GOBIN` if set. That folder must be on `PATH` for `serve_broker` and `neurokit.call`.

## Write the commands

Put the broker side in one module. `NOTES_DIR` is a variable set in the broker container, and this module keeps its notes there. Each function marked `@broker.command` is a command, and its type hints set the limits the broker checks before your code runs:

```python
# notes.py
"""Saves and reads short notes in NOTES_DIR."""

import json
import os
from pathlib import Path
from typing import Annotated

from neurokit import (
    SENT,
    VERIFIED,
    Broker,
    Done,
    Fail,
)
from pydantic import Field

Name = Annotated[
    str, Field(min_length=1, max_length=40)
]
Text = Annotated[str, Field(max_length=200)]

broker = Broker(
    "notes",
    broker_url_env="NOTES_URL",
    max_handlers=2,
)
LIMITS = dict(
    deadline_ms=5_000,
    max_body_bytes=1_000,
    max_response_bytes=1_000,
)


def store_path():
    """Returns the file that holds every note."""
    folder = Path(os.environ["NOTES_DIR"])
    return folder / "notes.json"


def load_notes():
    """Returns every saved note, by name."""
    store = store_path()
    if not store.exists():
        return {}
    return json.loads(store.read_text())


@broker.command(errors=["no-note"], **LIMITS)
def read_note(name: Name) -> dict:
    """Returns the note called name."""
    notes = load_notes()
    if name not in notes:
        raise Fail("no-note", f"no note {name}")
    return {"text": notes[name]}


@broker.command(
    mutating=True,
    lock="write",
    **LIMITS,
)
def save_note(name: Name, text: Text) -> Done:
    """Saves the note and reads it back."""
    notes = load_notes()
    notes[name] = text
    store_path().write_text(json.dumps(notes))
    if load_notes().get(name) != text:
        raise Fail(
            "verification-failed",
            "the note did not read back",
            write=SENT,
        )
    return Done({"saved": name}, write=VERIFIED)
```

The command's name is the function's name with `_` changed to `-`, so `save_note` is `save-note`. `Fail` takes a [shared code](../../README.md#python-tools), such as `verification-failed`, or one listed in the command's `errors`. Any other exception is a crash, which the caller gets as `unexpected`, or `change-unknown` for a mutating command.

A command with `mutating=True` changes something outside the broker, so it must say how far its change got. Return `Done(..., write=VERIFIED)` only after you read the change back, and `write=NOT_SENT` only when nothing was sent. When a change may have happened, never say `NOT_SENT`, because callers treat it as safe to retry.

`lock="write"` runs `save-note` alone, so two saves cannot overwrite each other's notes.

## Write the manifest

The broker and the caller both read the tool's `manifest.json`. Generate it from the module, and commit it next to the code:

```sh
python -m neurokit manifest notes manifest.json
```

Run this again after you change a command, its arguments or a limit. In the broker container, run `neurokit serve manifest.json` from the folder that holds `notes.py`. The `python3` that serve runs needs neurokit installed, and `NOTES_DIR` must be set.

## Call it

Put the caller side in its own module. In `notes.py`, `broker_url_env` names the variable that holds the broker's URL on the caller's side, here `NOTES_URL`. `neurokit.call` runs the `neurokit` binary, which sends the command to that URL:

```python
# notes_cli.py
"""Reads and saves notes through the broker."""

from pathlib import Path

import neurokit

MANIFEST = Path(__file__).with_name(
    "manifest.json"
)


def read(name):
    """Returns the note's text, or None."""
    try:
        reply = neurokit.call(
            MANIFEST,
            "read-note",
            name=name,
        )
    except neurokit.Fail as error:
        if error.code == "no-note":
            return None
        raise
    return reply["text"]


def save(name, text):
    """Saves the note and returns the result.

    The result is save-note's, such as
    {"saved": "todo"}.
    """
    done = neurokit.call(
        MANIFEST,
        "save-note",
        name=name,
        text=text,
    )
    return done.result
```

A failure raises `neurokit.Fail`. Retry a change only when `error.write` is `neurokit.NOT_SENT`.

## Test it

Test the tool against a real broker, not a mock, so the broker's own checks run. `serve_broker` starts `neurokit serve` on a free port and yields its URL:

```python
# test_notes.py
"""Runs the notes tool behind a real broker."""

import pytest
from neurokit.testing import serve_broker

import notes_cli


@pytest.fixture
def broker(tmp_path, monkeypatch):
    """Serves notes, keeping them in tmp_path."""
    monkeypatch.setenv("NOTES_DIR", str(tmp_path))
    manifest = notes_cli.MANIFEST
    with serve_broker(manifest) as url:
        monkeypatch.setenv("NOTES_URL", url)
        yield


def test_saved_note_reads_back(broker):
    """A confirmed save is what reads see.

    Callers trust a verified save, so a read
    right after it must return the note.
    """
    saved = notes_cli.save("todo", "milk")

    assert saved == {"saved": "todo"}
    assert notes_cli.read("todo") == "milk"


def test_missing_note_reads_as_none(broker):
    """read maps the no-note code to None.

    Callers check for None, so a missing note
    must not raise.
    """
    assert notes_cli.read("todo") is None
```

Run the tests from the folder that holds `notes.py`, because the broker's handlers import it from there:

```sh
python -m pytest test_notes.py
```
