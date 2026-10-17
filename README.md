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

## Layout

The binary is `cmd/neurokit`. All other Go code is in `internal/` packages; there is no public Go library. Each package owns one concern:

- `internal/wire`: what goes over HTTP: the routes, the `Neurokit-Spec` header, the reply body with the shared error codes and their HTTP statuses, the `write` field, and strict JSON decoding. A handler's answer has the same shape; it writes it on file descriptor 3, an extra pipe that only its answer uses.
- `internal/manifest`: the manifest format: reading and validating `manifest.json`, and checking each request's arguments and approval label against it.
- `internal/broker`: turning an HTTP request into a handler run and a reply: request checks, handler slots and the write lock, and building the reply from what the handler did.
- `internal/runner`: running one handler process and reporting what it did: its own process group, killed at the deadline and after it exits, the answer on file descriptor 3, capped output logs and a private `TMPDIR`.
- `internal/cli`: the client: checking a command's arguments against the manifest before sending them, and turning the broker's reply, or the lack of one, into stdout, stderr and an exit status.
- `cmd/neurokit`: the binary: `serve` checks the manifest and the tool's `check` program, connects the broker to the runner, and stops both on `SIGTERM`. `call` goes to `internal/cli`.

Packages import each other in one direction only. `internal/imports_test.go` lists what each package may import, and fails when a package imports an internal package outside its row or has no row at all.

## Development

The module needs Go 1.27.2, or Go 1.21 or later with the default `GOTOOLCHAIN=auto`, which downloads it. Run the same checks as CI; `gofmt -l .` prints nothing when every file is formatted:

```sh
gofmt -l .
go vet ./...
go test ./...
```

`.github/workflows/ci.yml` runs them on every pull request and every push to `main`.

The container test needs Docker on Linux: `sh test/container/run.sh`. It runs `serve` in a Python image with a real handler, and checks that `serve` refuses to start without an init process, leaves no processes behind and stops quickly on `docker stop`. It prints each request's time; CI runs it as the `container` job.

## License

neurokit is licensed under the MIT license. See [LICENSE](LICENSE).
