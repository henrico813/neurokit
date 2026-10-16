#!/bin/sh
# Runs serve in Docker with a real Python handler, as in a tool's container.
set -eu
cd "$(dirname "$0")/../.."
image=neurokit-container-test
container=neurokit-container-test-$$
trap 'docker rm -f "$container" "$container-no-init" >/dev/null 2>&1 || true' \
  EXIT
fail() {
  echo "FAIL: $*" >&2
  docker container inspect "$container" >/dev/null 2>&1 &&
    docker logs "$container" 2>&1 | tail -n 20 >&2
  exit 1
}

docker build -q -t "$image" -f test/container/Dockerfile . >/dev/null

# Without --init, serve is PID 1, which must clean up every finished
# process whose parent has gone. serve does not, so it must refuse to start;
# timeout stops one that starts anyway.
if no_init_output=$(timeout 20 docker run --name "$container-no-init" \
  "$image" 2>&1); then
  fail "serve started without an init process"
fi
echo "$no_init_output" | tail -n 1 | grep -q 'PID 1' ||
  fail "no PID 1 refusal: $no_init_output"

docker run -d --init --name "$container" -p 127.0.0.1::8080 "$image" >/dev/null
url=http://$(docker port "$container" 8080/tcp | head -n 1)
curl -fsS --retry 30 --retry-delay 1 --retry-all-errors "$url/readyz" \
  >/dev/null 2>&1 || fail "serve never became ready"
for i in 1 2 3; do
  curl -fsS -o /dev/null -w "ask $i: %{time_total} s\n" \
    -H 'Neurokit-Spec: 1' -d '{"question": "what is neurokit?"}' \
    "$url/commands/ask" || fail "ask $i failed"
done

# Each handler leaves a sleep behind: serve must kill it, then the init
# process must clean it up, which can take a moment. The search lists any
# process still named sleep, or finished but not cleaned up (state Z).
list_leftover_processes() {
  docker exec "$container" sh -c \
    'cd /proc && grep -s -e "^Name:.sleep" -e "^State:.Z" [0-9]*/status ||
      true'
}
tries=0
while :; do
  leftovers=$(list_leftover_processes) ||
    fail "serve exited before the leftover check"
  [ -n "$leftovers" ] || break
  tries=$((tries + 1))
  [ "$tries" -lt 10 ] || fail "leftover or zombie processes: $leftovers"
  sleep 0.2
done

# docker stop kills serve 10 s after SIGTERM, so a serve that ignores
# SIGTERM fails the 5 s limit. The serve_stop line shows serve shut down on
# its own instead of just dying.
stop_start_ns=$(date +%s%N)
docker stop "$container" >/dev/null
stop_ms=$((($(date +%s%N) - stop_start_ns) / 1000000))
echo "docker stop: $stop_ms ms"
[ "$stop_ms" -lt 5000 ] || fail "serve took $stop_ms ms to stop"
docker logs "$container" 2>&1 | grep -q '"msg":"serve_stop"' ||
  fail "no serve_stop log line"
echo PASS
