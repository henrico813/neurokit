# How a request travels through neurokit

This page follows one request from arrival to its log entries, then lists what can go wrong. Words such as handler, slot and `write` are explained in the [README's Terms](../../README.md#terms).

The example is the `demo` tool's `swap` command, which moves a player in a lineup. It is `mutating` and `lock: write`, accepts an approval label, and has a 5 s deadline.

## Before the first request

`neurokit serve` refuses to run as PID 1, the container's first process, where finished handler processes are never cleaned up; start the container with an init process, such as `docker run --init` or `init: true` in Compose. It also runs the tool's `check` program once and stops if it fails.

Go's limit on sending a reply starts before the body arrives, so serve sets it to the longest deadline plus the time to read the body and stop a handler. A shorter limit could lose the reply to a change that was made.

## The request arrives

The client sends:

```http
POST /commands/swap HTTP/1.1
Neurokit-Spec: 1

{
  "position": 3,
  "approval": "policy:demo-autofix"
}
```

The broker gives it an ID, such as `req-3f9c2a7b1d4e8f60`, then checks the route, method, query string, `Neurokit-Spec` header, body size and arguments, in that order. A refused mutation says `"write": "not-sent"`, because no handler ran.

The broker then takes out the approval label, which goes only into the audit log entry. The handler never sees it, so a tool cannot act differently because of what an agent wrote there.

## Waiting for its turn

The deadline starts now, so waiting counts against it. `swap` is `lock: write`, so it takes every slot and runs alone. Slots go out in arrival order, so reads cannot keep a write waiting forever.

## The handler runs

The handler starts in its own process group, so the broker can stop it and everything it starts at once. It reads its arguments, `{"position":3}`, on stdin.

It writes its answer to a separate pipe, with `>&3` in a shell script. Its stdout and stderr go to the log, so a stray print cannot break the answer.

Here it moves the player, confirms the change with the provider, and answers before it exits with 0:

```json
{
  "result": {"position": 3},
  "write": "verified"
}
```

## The handler finishes

When the handler exits, the broker kills its whole process group, even after a success, so nothing it started outlives the request. Then the slots are freed.

## The reply goes back

A mutation's reply always says what happened to the change. If the answer leaves that unclear, or the handler crashed, the reply is `change-unknown` with `"write": "unknown"`: read the provider's state before trying again. Here the client gets status 200 with the result and `"write": "verified"`.

## The log entries

serve logs JSON lines to stderr: each line the handler printed, an `audit` entry with the approval label, and a `request` entry. They all carry the request ID, as do error replies, so one search finds everything.

## When things go wrong

- **Client hangs up while waiting:** the request is dropped and never runs.
- **Client hangs up after the handler starts:** the handler finishes anyway, so a change is never cut off halfway.
- **Deadline passes:** a waiting request replies `deadline-exceeded`, and a mutation says `not-sent`. A running handler is asked to stop, then killed if still running 2 s later; unless it answered, a mutation then replies `change-unknown`. Nothing is retried.
- **Broker stops:** `docker stop` or Ctrl-C drops waiting requests and stops running handlers as at a deadline. serve exits within about 3 s.

## Health checks

`GET /livez` replies ok while serve runs. `GET /readyz` also runs the manifest's `ready` program, if any, and replies `not-ready` if it fails or takes over 1 s.
