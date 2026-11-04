#!/bin/sh
# Runs web/e2e/player-controls.mjs against a real ply: makes a 40 s clip with
# two audio tracks and a subtitle, serves it from a throwaway library and data
# directory, and stops the server and removes everything afterwards.
#
#   PLY_BIN=bin/ply sh scripts/e2e-player.sh      (or: make e2e-player)
#
# PLY_E2E_PORT picks the port (default 8472). Needs ffmpeg and Google Chrome.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
bin=${PLY_BIN:-$root/bin/ply}
port=${PLY_E2E_PORT:-8472}
base=http://127.0.0.1:$port

[ -x "$bin" ] || { echo "no ply binary at $bin (run make build)"; exit 2; }
if curl -s -o /dev/null "$base/api/info"; then
  echo "port $port is already in use; set PLY_E2E_PORT"; exit 2
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/ply-e2e-player.XXXXXX")
pid=
cleanup() {
  if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

mkdir -p "$work/media/Films" "$work/data"
printf '1\n00:00:01,000 --> 00:00:20,000\nHello\n' > "$work/sub.srt"
ffmpeg -v error -y -f lavfi -i testsrc2=s=1280x720:r=24:d=40 -f lavfi -i sine=f=440:d=40 \
  -f lavfi -i sine=f=660:d=40 -i "$work/sub.srt" -map 0 -map 1 -map 2 -map 3 \
  -c:v libx264 -preset ultrafast -g 48 -pix_fmt yuv420p -c:a aac -c:s srt \
  "$work/media/Films/Two Tracks (2020).mkv"
# A file modified seconds ago is still settling and the first scan skips it;
# nothing rescans here, so make it old.
touch -t 202001010000 "$work/media/Films/Two Tracks (2020).mkv"

"$bin" -addr "127.0.0.1:$port" -media "$work/media" -data "$work/data" \
  -metadata=false -watch=false -scan-interval 0 > "$work/ply.log" 2>&1 &
pid=$!

# Wait for the first scan to list the clip.
i=0
until curl -s "$base/api/items" 2>/dev/null | grep -q '"id"'; do
  i=$((i + 1))
  if [ "$i" -gt 150 ] || ! kill -0 "$pid" 2>/dev/null; then
    echo "ply did not come up; its log:"; cat "$work/ply.log"; exit 1
  fi
  sleep 0.2
done

cd "$root/web"
PLY_E2E_URL=$base node e2e/player-controls.mjs
