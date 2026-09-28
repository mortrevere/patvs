#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
test_root=$(mktemp -d)
test_bin="$test_root/patvs"
api_port=${PATVS_TEST_API_PORT:-27411}
discovery_port=${PATVS_TEST_DISCOVERY_PORT:-27412}
stream_port=${PATVS_TEST_STREAM_PORT:-27413}
secret=integration-secret

cleanup() {
	for pid in ${green_pid:-} ${red_pid:-} ${receiver_pid:-}; do
		kill "$pid" 2>/dev/null || true
	done
	wait 2>/dev/null || true
	rm -r "$test_root"
}
trap cleanup EXIT INT TERM

go build -o "$test_bin" ./cmd/patvs
"$test_bin" receiver --name test-receiver --state "$test_root/receiver.json" \
	--snapshot-dir "$test_root/snapshots" --listen ":$api_port" \
	--stream-listen "127.0.0.1:$stream_port" --discovery-port "$discovery_port" \
	--secret "$secret" >"$test_root/receiver.log" 2>&1 &
receiver_pid=$!

"$test_bin" emitter --name red --state "$test_root/red.json" \
	--camera 'lavfi:color=c=red:s=640x480:r=25' --seeds "127.0.0.1:$api_port" \
	--discovery-port "$discovery_port" --secret "$secret" >"$test_root/red.log" 2>&1 &
red_pid=$!
"$test_bin" emitter --name green --state "$test_root/green.json" \
	--camera 'lavfi:color=c=green:s=640x480:r=25' --seeds "127.0.0.1:$api_port" \
	--discovery-port "$discovery_port" --secret "$secret" >"$test_root/green.log" 2>&1 &
green_pid=$!

ready=false
for attempt in $(seq 1 20); do
	if "$test_bin" controller --secret "$secret" --seeds "127.0.0.1:$api_port" \
		--discovery-port "$discovery_port" emitters "127.0.0.1:$api_port" 2>/dev/null | grep -q 'online=true' && \
		[ "$("$test_bin" controller --secret "$secret" --seeds "127.0.0.1:$api_port" \
		--discovery-port "$discovery_port" emitters "127.0.0.1:$api_port" 2>/dev/null | grep -c 'online=true')" -eq 2 ]; then
		ready=true
		break
	fi
	sleep 1
done
[ "$ready" = true ] || { cat "$test_root"/*.log; exit 1; }

red_path=$("$test_bin" controller --secret "$secret" --seeds "127.0.0.1:$api_port" \
	--discovery-port "$discovery_port" snapshot "127.0.0.1:$api_port" red | sed -n 's/^path: //p')
green_path=$("$test_bin" controller --secret "$secret" --seeds "127.0.0.1:$api_port" \
	--discovery-port "$discovery_port" snapshot "127.0.0.1:$api_port" green | sed -n 's/^path: //p')

set -- $(ffmpeg -hide_banner -loglevel error -i "$red_path" -vf scale=1:1 -f rawvideo -pix_fmt rgb24 pipe:1 | od -An -tu1)
[ "$1" -gt 200 ] && [ "$2" -lt 30 ] && [ "$3" -lt 30 ]
set -- $(ffmpeg -hide_banner -loglevel error -i "$green_path" -vf scale=1:1 -f rawvideo -pix_fmt rgb24 pipe:1 | od -An -tu1)
[ "$1" -lt 30 ] && [ "$2" -gt 100 ] && [ "$3" -lt 30 ]

if "$test_bin" controller --secret wrong --seeds "127.0.0.1:$api_port" \
	--discovery-port "$discovery_port" status "127.0.0.1:$api_port" >/dev/null 2>&1; then
	echo "receiver accepted a wrong secret" >&2
	exit 1
fi

echo "patvs integration test passed"
