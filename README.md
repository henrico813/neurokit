# neurokit

neurokit will be one Go binary for the boundary between an AI assistant's command-line tools and the broker containers that hold their credentials. Each tool lists its commands in a `manifest.json`. In a broker container, `neurokit serve` will check every request against that manifest and run the tool's handler program for it, in its own process group and under a hard deadline. On the assistant's side, the same binary will be the CLI.

neurokit has no release yet, and the binary does not exist yet. This repository is being built in small reviewed steps toward `v0.1.0`. So far it holds the wire format: the error body, the shared error codes, the `write` field that says whether a change reached the outside service, and strict JSON decoding. It also holds the manifest package, which reads a tool's `manifest.json` and checks each request's arguments against it. And it holds the broker's HTTP core, which checks each request and turns what a handler did into the reply; nothing starts handler processes yet.

## Terms

- **Broker:** the HTTP server in a tool's container. It holds the tool's credentials; the assistant's container, where the CLI runs, holds none.
- **Handler:** the tool's own program. The broker runs it once per request. It reads its arguments as one JSON object on stdin and writes its answer on file descriptor 3.
- **Manifest:** a tool's `manifest.json`. It lists the tool's commands, their arguments and their limits: `max_handlers`, the most handlers that run at once, for the whole tool, and `deadline_ms`, `max_body_bytes` and `max_response_bytes` for each command.
- **Slot:** each handler uses memory, about 67 MiB for a real one, so a tool runs at most `max_handlers` handlers at once, one per slot. A command takes one slot while its handler runs and waits when none is free; a `lock: write` command takes every slot, so it runs alone.
- **Spec version:** one number, `spec` in the manifest, that versions both the manifest format and the HTTP wire between client and broker. The client sends it in the `Neurokit-Spec` header on every command request. A broker on another version replies `spec-mismatch` and runs nothing; the fix is to rebuild the older image.
- **`mutating` and `lock: write`** are separate settings. `mutating: true` means the command changes something outside the broker, so its replies carry a `write` field. `lock: write` means the command runs alone, with no other handler running. A mutating command without `lock: write` runs beside reads.
- **`write`** says whether a mutating command's change reached the provider, the outside service the tool talks to: `verified` (the provider is confirmed to match the request), `sent` (the request reached the provider, but its state is not confirmed), `not-sent` (nothing reached the provider) or `unknown`.
- **`change-unknown`** is the error code that comes with `write: unknown`: the change may or may not have happened, so read the provider's state before doing anything else.
- **Approval label:** an optional `approval` field of up to 200 characters, such as `policy:fantasy-autofix`, that a command with `approval: true` accepts. It is only a record: the broker writes it to the audit log entry and removes it before the handler runs, so no tool can treat it as permission.
- **Audit log entry:** the `audit` entry the broker logs for every mutating command that reached its handler, whether it succeeded or failed: the request ID, command, approval label, code and `write`.
- **`not-sent` must never be a guess.** Callers treat it as safe to retry, so the broker sends it only when it refused a request before any handler ran, or when the handler itself reported it.

## Layout

All Go code is in `internal/` packages; there is no public Go library. Each package owns one concern:

| Package | Owns |
| --- | --- |
| `internal/wire` | What goes over HTTP, and what a handler writes on file descriptor 3, an extra pipe that only its answer uses: the error body, the shared error codes and their HTTP statuses, the `write` field, and strict JSON decoding |
| `internal/manifest` | The manifest format: reading and validating `manifest.json`, and checking each request's arguments against it |
| `internal/broker` | Turning an HTTP request into a handler run and a reply: routes, request checks, handler slots and the write lock, and building the reply from what the handler did |

Packages import each other in one direction only. `internal/imports_test.go` lists what each package may import, and fails when a package imports an internal package outside its row or has no row at all.

## Development

The module needs Go 1.27.2, or Go 1.21 or later with the default `GOTOOLCHAIN=auto`, which downloads it. Run the same checks as CI:

```sh
gofmt -l .      # prints nothing when every file is formatted
go vet ./...
go test ./...
```

`.github/workflows/ci.yml` runs them on every pull request and every push to `main`.

## License

neurokit is licensed under the MIT license. See [LICENSE](LICENSE).
