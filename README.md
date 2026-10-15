# neurokit

neurokit will be one Go binary for the boundary between an AI assistant's command-line tools and the broker containers that hold their credentials. Each tool lists its commands in a `manifest.json`. In a broker container, `neurokit serve` will check every request against that manifest and run the tool's handler program for it, in its own process group and under a hard deadline. On the assistant's side, the same binary will be the CLI.

neurokit has no release yet, and the binary does not exist yet. This repository is being built in small reviewed steps toward `v0.1.0`. So far it holds the wire format: the error body, the shared error codes, the `write` field that says whether a change reached the outside service, and strict JSON decoding. It also holds the manifest package, which reads a tool's `manifest.json` and checks each request's arguments against it.

## Layout

All Go code is in `internal/` packages; there is no public Go library. Each package owns one concern:

| Package | Owns |
| --- | --- |
| `internal/wire` | What goes over HTTP, and what a handler writes on file descriptor 3, an extra pipe that only its answer uses: the error body, the shared error codes and their HTTP statuses, the `write` field, and strict JSON decoding |
| `internal/manifest` | The manifest format: reading and validating `manifest.json`, and checking each request's arguments against it |

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
