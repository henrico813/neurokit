# How a request travels through neurokit

This page follows one request from the moment a client sends it to the moment its reply and log entries are written. Then it shows what changes when the client hangs up, the deadline passes, or the broker stops. Words such as handler, slot and `write` are explained in the [README's Terms](../../README.md#terms).

The example tool is called `demo`. Its manifest has a command `swap` that moves a player into a lineup position. `swap` changes something outside the broker, so it is `mutating`. It is `lock: write`, so it runs alone, and `approval: true`, so it accepts an approval label. Its `deadline_ms` is 5000.

## Before the first request

In the tool's container, `neurokit serve /opt/demo/manifest.json` is the main process. Before it accepts any request, it does four things, and stops with one line saying why if any of them fails:

1. It reads the manifest and checks it. A command without its limits, a misspelled field or a reserved name stops serve here.
2. It refuses to run as PID 1, the first process in the container. When a process exits, its parent must collect it, and a process whose parent has gone is handed to PID 1. serve does not collect those, so they would stay in the process table for as long as the container runs. The container needs an init process, such as `docker run --init` or `init: true` in Compose, to be PID 1 instead.
3. It runs the manifest's `check` program once, the same way it runs handlers. If `check` exits with anything but 0, serve stops and prints the last line the program wrote to stderr, such as `neurokit serve: check failed: GEMINI_API_KEY is required`.
4. It logs a `serve_start` entry with its version and the manifest's `spec`, and listens on `0.0.0.0:8080`.

Each connection gets three time limits. A client has 5 s to send its headers and 10 s to send the whole request, so a stalled client cannot hold a connection open. Sending the reply may take the longest `deadline_ms` plus 13 s: Go starts that clock before the body arrives, so it covers the 10 s for the body, 2 s to stop a late handler, and 1 s spare.

## The request arrives

The client sends:

```http
POST /commands/swap HTTP/1.1
Neurokit-Spec: 1

{"position": 3, "approval": "policy:demo-autofix"}
```

The broker gives the request an ID, such as `req-3f9c2a7b1d4e8f60`, and checks it in this order:

1. The route names a command in the manifest. If not: 404 `not-found`.
2. The method is `POST`. If not: 405 `method-not-allowed`.
3. There is no query string. If there is: 400 `invalid-request`.
4. The `Neurokit-Spec` header matches the manifest's `spec`. If not: 400 `spec-mismatch`, which means one side runs an older binary and needs rebuilding. This comes before the body, so the caller is told to rebuild, not that the body is wrong.
5. The body is at most `max_body_bytes`. If not: 413 `request-too-large`.
6. The body is one JSON object with exactly the arguments the manifest declares, each within its bounds. If not: 400 `invalid-request`.

Every refusal is a JSON error body that carries the request ID. A refused mutation also says `"write": "not-sent"`, because no handler ran.

Last, the broker takes out the `approval` label. It goes into the audit log entry later. The handler never sees it, so a tool cannot act differently because of what an agent wrote there.

## Waiting for a slot

The deadline starts now. The 5 s cover the wait for a slot and the handler's run together.

A tool runs at most `max_handlers` handlers at once, one per slot. A `lock: write` command such as `swap` needs every slot, so it waits until all running handlers have finished. Requests get their slots in the order they arrived, so later reads cannot keep a waiting write from ever starting.

Two things can end the wait before the handler starts:

- **The deadline passes.** The reply is 504 `deadline-exceeded`, and for a mutation `"write": "not-sent"`, because nothing ran.
- **The client hangs up.** The request is dropped with 503 `not-ready` and `"write": "not-sent"`. The client is gone and never reads it, but the `request` log entry records it. A change is never made after its caller gave up waiting.

## The handler runs

With its slots taken, the broker hands the request to the runner, which starts the manifest's `program`. The handler gets:

- **Its own process group.** Everything the handler starts, such as an `ssh` that `git` starts, is in that group, so the runner can stop all of them at once.
- **Its arguments on stdin,** as one JSON object: `{"position":3}`.
- **An answer pipe.** The handler writes its answer to a separate pipe (`>&3` in a shell script), and its stdout and stderr go to the log. That way a stray print cannot break the answer, as one did in an early prototype.
- **The container's environment,** which is where its secrets come from, plus `NEUROKIT_MANIFEST`, `NEUROKIT_COMMAND` (`swap`), `NEUROKIT_REQUEST_ID`, `NEUROKIT_DEADLINE_MS` (the milliseconds left of the deadline) and `NEUROKIT_MUTATING` (`1`). It also gets its own empty `TMPDIR`.

Each line the handler writes to stdout or stderr becomes one `handler_output` log entry with the request ID, the command and the stream. A handler may log 64 KiB per request; past that, lines are dropped, and one `handler_output_dropped` entry says how many bytes were dropped.

From here on, a client hang-up no longer stops anything. The handler finishes, so a change is never cut off halfway because its caller went away, and the audit log entry records how it ended.

Here the handler moves the player, checks that the provider shows the new lineup, and writes this answer before it exits with 0:

```json
{"result": {"position": 3}, "write": "verified"}
```

## The handler finishes

When the handler exits, the runner kills its whole process group, even after a success, because nothing the handler started should outlive its request. Then it reads what is left in the pipes for at most 200 ms, because a process that left the group could hold one open for as long as it lives. Of the answer, it keeps at most `max_response_bytes` plus one byte, so an answer that is too large shows up as too large instead of cut short. It removes the handler's `TMPDIR`. Only then does the broker free the slots.

## The reply

The broker trusts only two kinds of answer: exit 0 with a `result`, or exit 1 with an `error`, either one a single JSON value within `max_response_bytes`. Anything else is a crash. An error's code must be one the command declares in the manifest, or one of the shared codes a handler may send, such as `provider-unavailable`; any other code becomes 500 `unexpected`.

A mutation's reply always says what happened to the change, in `write`. If the answer leaves that unclear, the reply becomes 502 `change-unknown` with `"write": "unknown"`, which tells the caller to read the provider's state before doing anything else. That happens when `write` is missing or `unknown`, when a success is only `sent`, after a crash, and in a few other contradictory cases.

Here the answer is clear, so the client gets:

```http
HTTP/1.1 200 OK
Content-Type: application/json

{"result":{"position":3},"write":"verified"}
```

A failure carries the request ID, so whoever sees it can find the log entries:

```json
{"error": {"code": "verification-failed", "message": "the provider did not confirm the swap", "request_id": "req-3f9c2a7b1d4e8f60"}, "write": "sent"}
```

## The log entries

serve logs JSON lines to stderr. For this request there are, in order:

- one `handler_output` entry for each line the handler printed;
- one `audit` entry, because `swap` is a mutation that reached its handler, with the request ID, the command, the approval label `policy:demo-autofix`, the code and `write`;
- one `request` entry with the request ID, the path, the code, `write`, the duration and, for a failure, a detail that only the log holds.

Searching the log for `req-3f9c2a7b1d4e8f60` finds all of them. Successful `/livez`, `/readyz` and `/manifest` requests are not logged, because health checks every few seconds would fill the log.

## When the deadline passes

If the handler is still running at the deadline, the runner sends its process group `SIGTERM`. A handler that has not exited 2 s later gets `SIGKILL`, and then the whole group does. A read replies 504 `deadline-exceeded`. A mutation replies 502 `change-unknown` with `"write": "unknown"`, because the change may already have reached the provider. Nothing is ever retried. So a `lock: write` command holds every slot for at most its deadline plus about 2 s.

## When the broker stops

`docker stop` sends serve `SIGTERM`, and Ctrl-C in a terminal does the same. serve stops accepting connections and tells every request to stop:

- A request still waiting for a slot is dropped, as after a hang-up: 503 `not-ready` with `"write": "not-sent"`. It never runs.
- A started handler keeps running when its client hangs up, so serve stops it itself, as at a deadline: `SIGTERM` to its group, then `SIGKILL` after 2 s. Unless it answers before it exits, a read then replies 500 `unexpected`, and a mutation replies 502 `change-unknown` with `"write": "unknown"`.

serve exits once every reply is written, and at most 3 s after the signal.

## Health checks

`GET /livez` does no work and always replies 200 `{"result":"ok"}`, so it shows only that serve is running. `GET /readyz` replies the same, unless the manifest names a `ready` program. Then it runs that program, which gets 1 s before it is stopped as at a deadline, and replies 503 `not-ready` if the program fails, with the program's last stderr line in the log. Compose calls `/readyz` every few seconds, so a `ready` program must be cheap, such as `["test", "-r", "/run/secrets/demo.env"]`. `GET /manifest` returns the manifest file as it was on disk when serve started.
