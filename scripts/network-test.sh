#!/bin/sh
set -eu

if [ "${1:-}" != "--inner" ]; then
	cd "$(dirname "$0")/.."
	test_root=$(mktemp -d)
	trap 'rm -r "$test_root"' EXIT INT TERM
	go build -o "$test_root/patvs" ./cmd/patvs
	exec unshare -Urn "$0" --inner "$test_root"
fi

test_root=$2
test_bin="$test_root/patvs"
ready="$test_root/emitter-ready"
secret=network-test
receiver_pid=
emitter_pid=

cleanup() {
	for pid in ${emitter_pid:-} ${receiver_pid:-}; do
		kill "$pid" 2>/dev/null || true
	done
	wait 2>/dev/null || true
	rm -r "$test_root"
}
trap cleanup EXIT INT TERM

ip link set lo up
unshare -n sh -c 'while [ ! -e "$1" ]; do sleep 0.05; done; exec "$2" emitter --name moving-camera --state "$3/emitter.json" --camera "lavfi:color=c=blue:s=640x480:r=25" --discovery-port 30413 --secret "$4" --debug' sh "$ready" "$test_bin" "$test_root" "$secret" >"$test_root/emitter.log" 2>&1 &
emitter_pid=$!

ip link add receiver-link type veth peer name emitter-link
ip link set emitter-link netns "$emitter_pid"
ip address add 10.55.0.1/24 dev receiver-link
ip link set receiver-link up
nsenter -t "$emitter_pid" -n ip link set lo up
nsenter -t "$emitter_pid" -n ip address add 10.55.0.2/24 dev emitter-link
nsenter -t "$emitter_pid" -n ip link set emitter-link up

"$test_bin" receiver --name moving-receiver --state "$test_root/receiver.json" \
	--snapshot-dir "$test_root/snapshots" --listen :7411 \
	--stream-listen 127.0.0.1:7413 --discovery-port 30412 --secret "$secret" \
	>"$test_root/receiver.log" 2>&1 &
receiver_pid=$!
touch "$ready"

connected=false
for attempt in $(seq 1 12); do
	if "$test_bin" controller --secret "$secret" emitters 10.55.0.1:7411 2>/dev/null | grep -q 'moving-camera.*online=true'; then
		connected=true
		break
	fi
	sleep 1
done
[ "$connected" = true ] || { cat "$test_root"/*.log; exit 1; }

kill "$receiver_pid"
wait "$receiver_pid" 2>/dev/null || true
ip address del 10.55.0.1/24 dev receiver-link
ip address add 10.55.0.3/24 dev receiver-link
recovery_started=$(date +%s)
"$test_bin" receiver --name moving-receiver --state "$test_root/receiver.json" \
	--snapshot-dir "$test_root/snapshots" --listen :7411 \
	--stream-listen 127.0.0.1:7413 --discovery-port 30412 --secret "$secret" \
	>"$test_root/receiver-restarted.log" 2>&1 &
receiver_pid=$!

reconnected=false
for attempt in $(seq 1 15); do
	if "$test_bin" controller --secret "$secret" emitters 10.55.0.3:7411 2>/dev/null | grep -q 'moving-camera.*online=true'; then
		reconnected=true
		break
	fi
	sleep 1
done
[ "$reconnected" = true ] || { cat "$test_root"/*.log; exit 1; }
recovery_seconds=$(($(date +%s) - recovery_started))
[ "$recovery_seconds" -le 15 ]

echo "patvs network fallback test passed; address-change recovery: ${recovery_seconds}s"
