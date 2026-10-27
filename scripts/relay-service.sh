# shellcheck shell=bash
# Managed systemd --user service commands for ./relay. Sourced by ./relay; uses its ROOT, RUNTIME, SERVICE, UNIT and helpers.
UNIT_MARKER='# Managed by ./relay setup. Rerun ./relay setup after editing this unit.'

is_managed(){ [[ -f "$UNIT" ]] && grep -Fqx "$UNIT_MARKER" "$UNIT"; }
user_systemctl(){ systemctl --user "$@" 9>&-; }
relay_env(){ go run ./cmd/relay-env "$@"; }
# Run without the operation lock so a legacy unit's ExecStop (./relay stop) can take it.
unlocked(){ flock -u 9; "$@"; flock -n 9 || fail 'Another Relay operation started; rerun the command'; }
is_missing(){ [[ " $(relay_env missing "$1") " == *" $1 "* ]]; }

ask(){
 local answer
 printf '%s' "$1" >&2
 if [[ "${2:-}" == secret ]]; then IFS= read -rs answer || fail 'No input; setup stopped'; echo >&2
 else IFS= read -r answer || fail 'No input; setup stopped'; fi
 printf '%s' "$answer"
}
url_encode(){
 local byte out=''
 for byte in $(printf '%s' "$1" | od -An -v -tx1); do
  case $byte in
   3[0-9]|4[1-9a-f]|5[0-9a]|6[1-9a-f]|7[0-9a]|2d|2e|5f|7e) printf -v byte '%b' "\\x$byte"; out+=$byte;;
   *) out+="%${byte^^}";;
  esac
 done
 printf '%s' "$out"
}

build_release(){
 mkdir -p "$RUNTIME/releases"
 RELEASE="$RUNTIME/releases/release-$(date +%Y%m%d-%H%M%S)-$$"
 mkdir "$RELEASE"
 go build -o "$RELEASE/relay-hub" ./cmd/relay-hub || { rm -rf "$RELEASE"; fail 'Go build failed; the running Hub was not changed'; }
}
build_release_ui(){
 build_ui || { rm -rf "$RELEASE"; fail 'UI build failed; the running Hub was not changed'; }
 cp -R frontend/dist "$RELEASE/ui"
}
activate(){ ln -sfn "releases/${1##*/}" "$RUNTIME/current.next"; mv -Tf "$RUNTIME/current.next" "$RUNTIME/current"; }
prune_releases(){
 local current old
 current=$(readlink "$RUNTIME/current")
 while IFS= read -r old; do
  [[ "releases/${old##*/}" == "$current" ]] || rm -rf -- "$old"
 done < <(find "$RUNTIME/releases" -maxdepth 1 -name 'release-*' -type d | sort -r | tail -n +4)
}
# Ready means this start of the unit (no automatic restart since) runs the current release and answers /readyz.
wait_ready(){
 local expected invocation pid i
 expected=$(readlink -f "$RUNTIME/current/relay-hub")
 invocation=$(systemctl --user show -p InvocationID --value "$SERVICE")
 for ((i=0;i<120;i++)); do
  [[ "$(systemctl --user show -p InvocationID --value "$SERVICE")" == "$invocation" ]] || return 1
  pid=$(systemctl --user show -p MainPID --value "$SERVICE")
  if [[ "$pid" != 0 && "$(readlink "/proc/$pid/exe" 2>/dev/null)" == "$expected" ]] &&
   curl -fsS --max-time 2 "http://$RELAY_ADDR/readyz" 2>/dev/null | grep -Fq '"status":"ready"'; then return 0; fi
  sleep .5
 done
 return 1
}
start_service(){ user_systemctl restart "$SERVICE" && wait_ready; }

configure_database(){
 local url
 if ! is_missing DATABASE_URL; then
  echo 'Database: using DATABASE_URL from .env'
  "$RELEASE/relay-hub" doctor || fail 'Cannot use DATABASE_URL from .env; fix it and rerun ./relay setup'
  return
 fi
 if [[ -n "$CONFIGURED_DATABASE_URL" ]]; then
  url=$CONFIGURED_DATABASE_URL
  DATABASE_URL=$url "$RELEASE/relay-hub" doctor || fail 'Cannot connect with the DATABASE_URL environment value'
 else
  while true; do
   url=$(ask_database_url)
   DATABASE_URL=$url "$RELEASE/relay-hub" doctor && break
   echo 'Connection failed. Check the details and try again.' >&2
  done
 fi
 printf '%s\n' "$url" | relay_env set DATABASE_URL
 export DATABASE_URL=$url
 echo 'Saved DATABASE_URL in .env'
}
ask_database_url(){
 local url host port name user password ssl=''
 url=$(ask 'PostgreSQL 18 URL (postgresql://user:password@host:5432/db), or Enter to give the parts: ' secret)
 if [[ -n "$url" ]]; then printf '%s' "$url"; return; fi
 host=$(ask 'Host [127.0.0.1]: '); host=${host:-127.0.0.1}
 port=$(ask 'Port [5432]: '); port=${port:-5432}
 name=$(ask 'Database [relay_hub]: '); name=${name:-relay_hub}
 user=$(ask 'User [relay_hub]: '); user=${user:-relay_hub}
 password=$(ask 'Password: ' secret)
 [[ "$host" == 127.0.0.1 || "$host" == localhost || "$host" == ::1 ]] && ssl='?sslmode=disable'
 [[ "$host" == *:* ]] && host="[$host]"
 printf 'postgresql://%s:%s@%s:%s/%s%s' "$(url_encode "$user")" "$(url_encode "$password")" "$host" "$port" "$(url_encode "$name")" "$ssl"
}
configure_origin(){
 is_missing RELAY_PUBLIC_ORIGIN || return 0
 local origin=${RELAY_PUBLIC_ORIGIN:-}
 until [[ "$origin" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?$ ]]; do
  [[ -z "$origin" ]] || echo 'Use https:// and a host name, without a path.' >&2
  origin=$(ask 'Public HTTPS address of the Hub (for example https://relay.example.com): ')
 done
 printf '%s\n' "$origin" | relay_env set RELAY_PUBLIC_ORIGIN
 export RELAY_PUBLIC_ORIGIN=$origin
 echo "Saved RELAY_PUBLIC_ORIGIN=$origin in .env"
}
configure_settings(){
 local added
 if is_missing RELAY_ADDR; then printf '%s\n' "$RELAY_ADDR" | relay_env set RELAY_ADDR; echo "Saved RELAY_ADDR=$RELAY_ADDR in .env"; fi
 added=$(relay_env secrets)
 [[ -z "$added" ]] || echo "Generated missing secrets: $added"
 configure_origin
}

unit_content(){
 cat <<UNIT
$UNIT_MARKER
[Unit]
Description=Relay Hub
After=network.target

[Service]
Type=exec
WorkingDirectory=$ROOT
Environment="GOMAXPROCS=$GOMAXPROCS" "RELAY_UI_DIR=$RUNTIME/current/ui"
ExecStart="$RUNTIME/current/relay-hub" serve
Nice=5
Restart=always
RestartSec=5
TimeoutStopSec=30
UMask=0077

[Install]
WantedBy=default.target
UNIT
}
stop_running_hub(){
 if user_systemctl is-active --quiet "$SERVICE"; then echo "Stopping $SERVICE"; unlocked user_systemctl stop "$SERVICE"; fi
 if owned_pid; then echo 'Stopping the Hub started by ./relay restart'; stop_hub; fi
}
install_unit(){
 local wanted
 wanted=$(unit_content)
 mkdir -p "${UNIT%/*}"
 if [[ -f "$UNIT" && "$(cat "$UNIT")" == "$wanted" ]]; then echo "Service unit $SERVICE is up to date"
 else
  if [[ -f "$UNIT" ]]; then cp "$UNIT" "$UNIT.bak"; echo "Replacing service unit $SERVICE (old copy: $UNIT.bak)"; else echo "Creating service unit $SERVICE"; fi
  printf '%s\n' "$wanted" >"$UNIT.next"; chmod 644 "$UNIT.next"; mv -f "$UNIT.next" "$UNIT"
  user_systemctl daemon-reload
 fi
 user_systemctl enable --quiet "$SERVICE"
}
enable_linger(){
 local user
 user=$(id -un)
 [[ "$(loginctl show-user "$user" -p Linger --value 2>/dev/null)" == yes ]] && return
 if loginctl enable-linger --no-ask-password "$user" 2>/dev/null; then echo 'Enabled linger so the Hub starts at boot'
 else echo "To start the Hub at boot, run once as an administrator: sudo loginctl enable-linger $user"; fi
}

setup(){
 prerequisites
 command -v systemctl >/dev/null || fail 'systemd is required for ./relay setup'
 chmod 600 .env
 [[ "${RELAY_DATABASE_MODE:-}" != docker ]] || start_db
 echo 'Building the Hub'
 build_release
 configure_database
 configure_settings
 echo 'Building the UI'
 build_release_ui
 stop_running_hub
 if ! "$RELEASE/relay-hub" migrate; then
  flock -u 9; user_systemctl start "$SERVICE" 2>/dev/null || true
  fail 'Migration failed; no change was applied. Fix the error above and rerun ./relay setup'
 fi
 activate "$RELEASE"
 install_unit
 enable_linger
 start_service || fail "Hub did not become ready; see ./relay logs"
 prune_releases
 echo "Relay Hub ready: http://$RELAY_ADDR (public: $RELAY_PUBLIC_ORIGIN)"
 printf '%s\n' 'Next steps:' \
  '1. Proxy the public HTTPS address to the address above.' \
  '2. Create the first administrator: ./relay create-admin' \
  '3. Keep a private copy of .env. It holds the credential key.'
}
managed_restart(){
 prerequisites; start_db
 build_release; build_release_ui
 go run ./cmd/relay-check || { rm -rf "$RELEASE"; fail 'Database is not ready for this code (pending migrations?); run ./relay update. The running Hub was not changed'; }
 local previous
 previous=$(readlink "$RUNTIME/current" || true)
 activate "$RELEASE"
 if start_service; then prune_releases; echo "Relay Hub ready: http://$RELAY_ADDR"; return; fi
 [[ -n "$previous" ]] || fail 'Hub did not become ready; see ./relay logs'
 activate "$RUNTIME/$previous"
 rm -rf "$RELEASE"
 start_service || fail 'New and previous releases both failed; see ./relay logs'
 fail 'New release did not become ready; the previous release is running again. See ./relay logs'
}
managed_update(){
 is_managed || fail 'Run ./relay setup once before ./relay update'
 if [[ "${1:-}" != --no-pull ]]; then
  git pull --ff-only 9>&- || fail 'git pull failed; nothing was changed'
  exec "$ROOT/relay" update --no-pull
 fi
 prerequisites; start_db
 build_release; build_release_ui
 "$RELEASE/relay-hub" doctor || { rm -rf "$RELEASE"; fail 'Database check failed; the running Hub was not changed'; }
 user_systemctl stop "$SERVICE"
 if ! "$RELEASE/relay-hub" migrate; then
  user_systemctl start "$SERVICE"
  fail 'Migration failed; no change was applied and the previous release was started again'
 fi
 activate "$RELEASE"
 start_service || fail 'Hub did not become ready after update; see ./relay logs'
 prune_releases
 echo "Relay Hub updated to $(git log --oneline -1) and ready"
}
managed_status(){
 user_systemctl status "$SERVICE" --no-pager --lines=0 || true
 curl -fsS --max-time 3 "http://$RELAY_ADDR/readyz" || fail 'Hub is not ready'
 echo
}
create_admin(){
 [[ -x "$RUNTIME/current/relay-hub" ]] || fail 'Run ./relay setup first'
 local name password again
 name=$(ask 'Administrator username: ')
 password=$(ask 'Password: ' secret)
 again=$(ask 'Repeat password: ' secret)
 [[ "$password" == "$again" ]] || fail 'Passwords do not match'
 printf '%s\n%s\n' "$name" "$password" | "$RUNTIME/current/relay-hub" bootstrap-user
}
