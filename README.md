# neurokit

neurokit is one Go binary for the boundary between an AI assistant's command-line tools and the broker containers that hold their credentials. Each tool lists its commands in a `manifest.json`.

In a broker container, `neurokit serve` checks every request against that manifest and runs the tool's handler program for it, in its own process group and under a hard deadline. On the assistant's side, the same binary is the client: see [Calling a broker](#calling-a-broker).

neurokit has no release yet; it is being built toward `v0.1.0`. [Layout](#layout) lists what each package holds so far.

[How a request travels](docs/explanation/request-lifecycle.md) follows one request through the broker and its handler, including what happens when the client hangs up, the deadline passes or the broker stops.

## Terms

- **Broker:** the HTTP server in a tool's container. It holds the tool's credentials; the assistant's container, where the CLI runs, holds none.
- **Handler:** the tool's own program. The broker runs it once per request. It reads its arguments as one JSON object on stdin and writes its answer on file descriptor 3, the answer pipe.
- **Manifest:** a tool's `manifest.json`. It lists the tool's commands, their arguments and their limits: `max_handlers`, the most handlers that run at once, for the whole tool, and `deadline_ms`, `max_body_bytes` and `max_response_bytes` for each command.
- **Slot:** each handler uses memory, about 67 MiB for a real one, so a tool runs at most `max_handlers` handlers at once, one per slot. A command takes one slot while its handler runs and waits when none is free; a `lock: write` command takes every slot, so it runs alone.
- **Spec version:** one number, `spec` in the manifest, that versions both the manifest format and the HTTP wire between client and broker. The client sends it in the `Neurokit-Spec` header on every command request. A broker on another version replies `spec-mismatch` and runs nothing; the fix is to rebuild the older image.
- **`mutating` and `lock: write`** are separate settings: `mutating: true` means the command changes something outside the broker, so its replies carry a `write` field. `lock: write` means the command runs alone, with no other handler running. A mutating command without `lock: write` runs beside reads.
- **`write`** says whether a mutating command's change reached the provider, the outside service the tool talks to: `verified` (the provider is confirmed to match the request), `sent` (the request reached the provider, but its state is not confirmed), `not-sent` (nothing reached the provider) or `unknown`.
- **`change-unknown`** is the error code that comes with `write: unknown`: the change may or may not have happened, so read the provider's state before doing anything else.
- **Approval label:** an optional `approval` field of up to 200 characters, such as `policy:fantasy-autofix`, that a command with `approval: true` accepts. It is only a record: the broker writes it to the audit log entry and removes it before the handler runs, so no tool can treat it as permission.
- **Audit log entry:** the `audit` entry the broker logs for every mutating command that reached its handler, whether it succeeded or failed: the request ID, command, approval label, code and `write`.
- **`not-sent` must never be a guess.** Callers treat it as safe to retry, so the broker sends it only when it refused a request before any handler ran, or when the handler itself reported it. The client sends it only when it never sent the request.

## Calling a broker

`neurokit call <manifest> <command> <json>` sends one command, with its arguments as one JSON object, to the broker at the URL in the variable that the manifest's `broker_url_env` names. A Python CLI runs it instead of making its own HTTP calls.

It prints one JSON object on stdout: the broker's reply, or a body the client built. It exits 0 for a result, 1 for an error, with one line on stderr, and 2 for bad arguments, which it checks before sending anything.

A mutation that gets no usable reply after connecting is `change-unknown`, because the broker may already have run it. It is `broker-unavailable` with `write: not-sent` only when the client never connected. A mutation reply whose `write` does not show whether the change happened is printed as `change-unknown` too.

`neurokit run <manifest> <command> --flags` sends the same request with flags built from the manifest: `slot_id` becomes `--slot-id`, a boolean is a bare flag, and a list is a repeated flag. An object, or a list of objects or lists, needs `call`. Both print and exit the same way.

`neurokit health <manifest>` asks the broker's `/readyz` and waits at most 3 s. When the URL variable is unset or empty, it asks `http://127.0.0.1:8080`, where `serve` listens inside a broker container. Its output and exit status follow `call`'s.

`neurokit help <manifest>` lists the commands, and `neurokit help <manifest> <command>` lists one command's flags; `--help` after a command prints that command's flags. With no manifest, `help` prints the version and the usage line.

## Python tools

`python/neurokit` is a Python library for writing a tool as plain functions. It writes the tool's manifest and answers the broker's requests, so the tool has no code of its own for either. It needs Python 3.12 or later and installs pydantic: `pip install ./python`.

A tool module defines one `Tool`. Each command is a function. The command's name is the function's name with `_` changed to `-`, so `save_greeting` becomes `save-greeting`.

Every argument needs a type hint. A `str` needs `Field(max_length=...)`, an `int` needs `Field(ge=..., le=...)`, and a list needs `Field(max_length=...)`, because the broker checks these limits before your code runs. An object argument is a `TypedDict`, and the function gets a dict.

A default value on the parameter makes the argument optional; `Field(default=...)` is refused.

```python
from typing import Annotated

from neurokit import Tool
from pydantic import Field

tool = Tool(
    "greeter",
    broker_url_env="GREETER_URL",
    max_handlers=1,
)


@tool.command(
    deadline_ms=5_000,
    max_body_bytes=1_000,
    max_response_bytes=1_000,
)
def greet(
    name: Annotated[str, Field(max_length=20)],
) -> dict:
    return {"greeting": f"hello {name}"}
```

Import `Done`, `Fail` and the `write` values from `neurokit`.

A function returns its result, or raises `Fail(code, message)` for a failure the caller should understand. Any other exception is a crash: the caller gets `unexpected` (`change-unknown` for a mutating command), and the traceback goes only to the broker's log.

`code` must be a shared code, one of `invalid-request`, `forbidden`, `not-found`, `unexpected`, `provider-unavailable`, `provider-rejected`, `change-unknown` and `verification-failed`, or one listed in the command's `errors=[...]`. Any other code reaches the caller as `unexpected`.

A mutating command (`mutating=True`) returns `Done(result, write=VERIFIED)` once it has confirmed the change, or `Done(result, write=NOT_SENT)` when it sent nothing, such as for a preview. A failure raises `Fail(..., write=NOT_SENT)` or `Fail(..., write=SENT)`, and `verification-failed` needs `SENT`.

Any other pairing reaches the caller as `change-unknown`: a plain return value, a `Done` with `SENT` or no `write`, a `Fail` with `VERIFIED` or no `write`, or `verification-failed` without `SENT`.

`python -m neurokit manifest greeter manifest.json` imports the module `greeter` and writes its manifest. Commit the file next to the code. If a type hint has no place in the manifest, such as `Field(min_length=1)`, the command prints one line naming the parameter and writes no file.

In the broker container, `neurokit serve manifest.json` runs `python3 -m neurokit handle greeter` for each request. So `python3` must have this library installed and must be able to import `greeter`, for example from the folder `serve` starts in or through `PYTHONPATH`.

`@tool.check` marks a function that `serve` runs once at start, such as reading a secret. If it raises, `serve` stops with the exception's last line as the reason.

On the caller's side, `neurokit.call(...)` runs the `neurokit call` binary, which must be on `PATH`. The variable that `broker_url_env` names, here `GREETER_URL`, must hold the broker's URL, as "Calling a broker" above describes.

It returns what the function returned (a `Done` for a mutating command). On an error, from the function, the broker or the binary, it raises `Fail`.

```python
import neurokit

result = neurokit.call(
    "/opt/greeter/manifest.json",
    "greet",
    name="Ann",
)
```

## Layout

The binary is `cmd/neurokit`. All other Go code is in `internal/` packages; there is no public Go library. Each package owns one concern:

- `internal/wire`: what goes over HTTP: the routes, the `Neurokit-Spec` header, the reply body with the shared error codes and their HTTP statuses, the `write` field, and strict JSON decoding. A handler's answer has the same shape; it writes it on file descriptor 3, an extra pipe that only its answer uses.
- `internal/manifest`: the manifest format: reading and validating `manifest.json`, and checking each request's arguments and approval label against it.
- `internal/broker`: turning an HTTP request into a handler run and a reply: request checks, handler slots and the write lock, and building the reply from what the handler did.
- `internal/runner`: running one handler process and reporting what it did: its own process group, killed at the deadline and after it exits, the answer on file descriptor 3, capped output logs and a private `TMPDIR`.
- `internal/cli`: the client: checking a command's arguments against the manifest before sending them, turning the broker's reply, or the lack of one, into stdout, stderr and an exit status, printing help, and asking whether a broker is ready.
- `cmd/neurokit`: the binary: `serve` checks the manifest and the tool's `check` program, connects the broker to the runner, and stops both on `SIGTERM`. `run`, `call`, `help` and `health` go to `internal/cli`.

Packages import each other in one direction only. `internal/imports_test.go` lists what each package may import, and fails when a package imports an internal package outside its row or has no row at all.

Outside Go, `python/neurokit` is the Python library, and `python/tests` holds its tests.

## Development

The module needs Go 1.27.2, or Go 1.21 or later with the default `GOTOOLCHAIN=auto`, which downloads it. Run the same checks as CI; `gofmt -l .` prints nothing when every file is formatted:

```sh
gofmt -l .
go vet ./...
go test ./...
```

The Python tests need Go too, because they build `serve` and start it on port 8080:

```sh
pip install ./python pytest
python -m pytest python/tests
```

`.github/workflows/ci.yml` runs them on every pull request and every push to `main`.

The container test needs Docker on Linux: `sh test/container/run.sh`. It runs `serve` in a Python image with a real handler, and checks that `serve` refuses to start without an init process, leaves no processes behind and stops quickly on `docker stop`. It prints each request's time; CI runs it as the `container` job.

## License

neurokit is licensed under the MIT license. See [LICENSE](LICENSE).
