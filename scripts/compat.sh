#!/usr/bin/env bash
# Starts or removes a container of a DBMS that is checked by hand (see compat:up
# in Taskfile.yml, which calls this script).
#
#   compat.sh up NAME     start the container and wait until the server answers
#   compat.sh down NAME   remove the container
#
# Environment: COMPAT_<DBMS>_IMAGE (set by the Taskfile), PORT (host port; empty
# for the default of NAME), HOST (address to dial the published port; default
# 127.0.0.1), TIMEOUT (seconds to wait; default 180).
set -euo pipefail

cmd="${1:-}"
name="${2:-}"
supported="tidb cockroach clickhouse libsql"

fail() {
  echo "$*" >&2
  exit 1
}

unsupported() {
  fail "unsupported NAME '${name}'; supported names: ${supported}"
}

# Sets image, cport (container port), dport (default host port) and run_args.
select_dbms() {
  run_args=()
  server_args=()
  case "$name" in
    tidb)
      image="${COMPAT_TIDB_IMAGE:?}"
      cport=4000
      dport=33081
      ;;
    cockroach)
      image="${COMPAT_COCKROACH_IMAGE:?}"
      cport=26257
      dport=33082
      server_args=(start-single-node --insecure)
      ;;
    clickhouse)
      image="${COMPAT_CLICKHOUSE_IMAGE:?}"
      cport=9000
      dport=33083
      # Lets the passwordless default user connect through the published port.
      run_args=(--ulimit nofile=262144:262144 -e CLICKHOUSE_SKIP_USER_SETUP=1)
      ;;
    libsql)
      image="${COMPAT_LIBSQL_IMAGE:?}"
      cport=8080
      dport=33084
      ;;
    *) unsupported ;;
  esac
}

# Succeeds once the server of $name answers on the published port.
ready() {
  case "$name" in
    tidb)
      # The MySQL protocol: the server sends its greeting as soon as it is up.
      bash -c 'exec 3<>"/dev/tcp/$0/$1" && head -c1 <&3 >/dev/null' "$host" "$port" 2>/dev/null
      ;;
    cockroach)
      docker exec "$container" ./cockroach sql --insecure -e 'select 1' >/dev/null 2>&1
      ;;
    clickhouse)
      docker exec "$container" clickhouse-client --query 'select 1' >/dev/null 2>&1
      ;;
    libsql)
      bash -c 'exec 3<>"/dev/tcp/$0/$1" && printf "GET /health HTTP/1.0\r\n\r\n" >&3 && head -n1 <&3 | grep -q " 200"' "$host" "$port" 2>/dev/null
      ;;
  esac
}

case "$cmd" in
  up)
    select_dbms
    container="mysqldance-compat-${name}"
    port="${PORT:-$dport}"
    host="${HOST:-127.0.0.1}"
    timeout="${TIMEOUT:-180}"
    docker rm -fv "$container" >/dev/null 2>&1 || true
    docker run -d --name "$container" ${run_args[@]+"${run_args[@]}"} -p "127.0.0.1:${port}:${cport}" "$image" ${server_args[@]+"${server_args[@]}"} >/dev/null
    echo "waiting for ${name} (${image}) on ${host}:${port}..."
    i=0
    until ready; do
      i=$((i + 1))
      if [ "$i" -ge "$timeout" ]; then
        echo "${name} did not become ready within ${timeout}s" >&2
        docker logs --tail 20 "$container" >&2
        docker rm -fv "$container" >/dev/null 2>&1 || true
        exit 1
      fi
      sleep 1
    done
    echo "${name} is ready: container ${container}, ${host}:${port}"
    ;;
  down)
    case " ${supported} " in
      *" ${name} "*) ;;
      *) unsupported ;;
    esac
    docker rm -fv "mysqldance-compat-${name}" >/dev/null 2>&1 || true
    ;;
  *)
    fail "usage: compat.sh up|down NAME (names: ${supported})"
    ;;
esac
