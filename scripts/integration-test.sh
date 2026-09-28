#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
test_root=$(mktemp -d)
test_bin="$test_root/patvs"
api_port=${PATVS_TEST_API_PORT:-27411}
discovery_port=${PATVS_TEST_DISCOVERY_PORT:-27412}
stream_port=${PATVS_TEST_STREAM_PORT:-27413}
api_port_2=$((api_port + 10))
discovery_port_2=$((discovery_port + 10))
stream_port_2=$((stream_port + 10))
secret=integration-secret

cleanup() {
	for pid in ${green_pid:-} ${red_pid:-} ${receiver_pid_2:-} ${receiver_pid:-}; do
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
"$test_bin" receiver --name test-receiver-2 --state "$test_root/receiver-2.json" \
	--snapshot-dir "$test_root/snapshots-2" --listen ":$api_port_2" \
	--stream-listen "127.0.0.1:$stream_port_2" --discovery-port "$discovery_port_2" \
	--secret "$secret" >"$test_root/receiver-2.log" 2>&1 &
receiver_pid_2=$!

receiver_seeds="127.0.0.1:$api_port,127.0.0.1:$api_port_2"

"$test_bin" emitter --name red --state "$test_root/red.json" \
	--camera 'lavfi:color=c=red:s=640x480:r=25' --seeds "$receiver_seeds" \
	--discovery-port "$discovery_port" --secret "$secret" >"$test_root/red.log" 2>&1 &
red_pid=$!
"$test_bin" emitter --name green --state "$test_root/green.json" \
	--camera 'lavfi:color=c=green:s=640x480:r=25' --seeds "$receiver_seeds" \
	--discovery-port "$discovery_port" --secret "$secret" >"$test_root/green.log" 2>&1 &
green_pid=$!

ready=false
for attempt in $(seq 1 20); do
	count_1=$("$test_bin" controller --secret "$secret" emitters "127.0.0.1:$api_port" 2>/dev/null | grep -c 'online=true' || true)
	count_2=$("$test_bin" controller --secret "$secret" emitters "127.0.0.1:$api_port_2" 2>/dev/null | grep -c 'online=true' || true)
	if [ "$count_1" -eq 2 ] && [ "$count_2" -eq 2 ]; then
		ready=true
		break
	fi
	sleep 1
done
[ "$ready" = true ] || { cat "$test_root"/*.log; exit 1; }

red_path=$("$test_bin" controller --secret "$secret" snapshot "127.0.0.1:$api_port" red | sed -n 's/^path: //p')
green_path=$("$test_bin" controller --secret "$secret" snapshot "127.0.0.1:$api_port" green | sed -n 's/^path: //p')
red_path_2=$("$test_bin" controller --secret "$secret" snapshot "127.0.0.1:$api_port_2" red | sed -n 's/^path: //p')

set -- $(ffmpeg -hide_banner -loglevel error -i "$red_path" -vf scale=1:1 -f rawvideo -pix_fmt rgb24 pipe:1 | od -An -tu1)
[ "$1" -gt 200 ] && [ "$2" -lt 30 ] && [ "$3" -lt 30 ]
set -- $(ffmpeg -hide_banner -loglevel error -i "$green_path" -vf scale=1:1 -f rawvideo -pix_fmt rgb24 pipe:1 | od -An -tu1)
[ "$1" -lt 30 ] && [ "$2" -gt 100 ] && [ "$3" -lt 30 ]
set -- $(ffmpeg -hide_banner -loglevel error -i "$red_path_2" -vf scale=1:1 -f rawvideo -pix_fmt rgb24 pipe:1 | od -An -tu1)
[ "$1" -gt 200 ] && [ "$2" -lt 30 ] && [ "$3" -lt 30 ]

kill "$red_pid"
wait "$red_pid" 2>/dev/null || true
"$test_bin" emitter --name red --state "$test_root/red.json" \
	--camera 'lavfi:color=c=red:s=640x480:r=25' --discovery-port "$discovery_port" \
	--secret "$secret" >"$test_root/red-restarted.log" 2>&1 &
red_pid=$!
remembered=false
for attempt in $(seq 1 20); do
	red_1=$("$test_bin" controller --secret "$secret" emitters "127.0.0.1:$api_port" 2>/dev/null | grep 'red' | grep -c 'online=true' || true)
	red_2=$("$test_bin" controller --secret "$secret" emitters "127.0.0.1:$api_port_2" 2>/dev/null | grep 'red' | grep -c 'online=true' || true)
	if [ "$red_1" -eq 1 ] && [ "$red_2" -eq 1 ]; then
		remembered=true
		break
	fi
	sleep 1
done
[ "$remembered" = true ] || { cat "$test_root"/*.log; exit 1; }

"$test_bin" controller --secret "$secret" stream "127.0.0.1:$api_port" red start >/dev/null
"$test_bin" controller --secret "$secret" stream "127.0.0.1:$api_port_2" red start >/dev/null
red_id=$(sed -n 's/^[[:space:]]*"id": "\([^"]*\)".*/\1/p' "$test_root/red.json" | sed -n '1p')
timeout 8s ffmpeg -hide_banner -loglevel error -i "http://127.0.0.1:$stream_port/streams/$red_id.mjpg" \
	-frames:v 1 -f null -
timeout 8s ffmpeg -hide_banner -loglevel error -i "http://127.0.0.1:$stream_port_2/streams/$red_id.mjpg" \
	-frames:v 1 -f null -

kill "$receiver_pid"
wait "$receiver_pid" 2>/dev/null || true
"$test_bin" receiver --name test-receiver --state "$test_root/receiver.json" \
	--snapshot-dir "$test_root/snapshots" --listen ":$api_port" \
	--stream-listen "127.0.0.1:$stream_port" --discovery-port "$discovery_port" \
	--secret "$secret" >"$test_root/receiver-restarted.log" 2>&1 &
receiver_pid=$!
restored=false
for attempt in $(seq 1 20); do
	if "$test_bin" controller --secret "$secret" status "127.0.0.1:$api_port" 2>/dev/null | grep -A3 '"streams"' | grep -q 'true'; then
		restored=true
		break
	fi
	sleep 1
done
[ "$restored" = true ] || { cat "$test_root"/*.log; exit 1; }

if "$test_bin" controller --secret wrong status "127.0.0.1:$api_port" >/dev/null 2>&1; then
	echo "receiver accepted a wrong secret" >&2
	exit 1
fi

echo "patvs integration test passed"
