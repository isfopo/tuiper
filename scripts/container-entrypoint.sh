#!/bin/bash
# Container entrypoint for the tuiper dev compose services.
#
# Starts a private PipeWire session (audio + MIDI) so audio engine code has
# real ALSA devices and sink/monitor capture with no host hardware, then
# execs the given command. Usage patterns handled by docker compose run:
#
#   docker compose run --rm dev                          -> exec bash
#   docker compose run --rm dev go test ./internal/...   -> exec go test ...
#   docker compose run --rm tuiper -engine test -demo    -> exec go run ./cmd/tuiper ...
#                                           ^ a leading flag is taken as a
#                                             tuiper flag and wrapped in go run
set -u

if command -v pipewire >/dev/null 2>&1; then
    export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/pipewire}"
    mkdir -p "$XDG_RUNTIME_DIR"
    chmod 700 "$XDG_RUNTIME_DIR" 2>/dev/null || true

    # wireplumber (session manager) wants a D-Bus session bus
    if command -v dbus-daemon >/dev/null 2>&1; then
        : "${DBUS_SESSION_BUS_ADDRESS:="unix:path=$XDG_RUNTIME_DIR/bus"}"
        export DBUS_SESSION_BUS_ADDRESS
        dbus-daemon --session --fork --address="$DBUS_SESSION_BUS_ADDRESS" --nopidfile >/dev/null 2>&1 || true
    fi

    # Debian bug #1019404: wireplumber aborts without systemd-logind (no
    # systemd in containers). Provide the stock override.
    BLUEZ_CFG=/usr/share/wireplumber/bluetooth.lua.d/50-bluez-config.lua
    if [ -f "$BLUEZ_CFG" ] && ! grep -q 'with-logind.*false' "$BLUEZ_CFG"; then
        sed -i 's/\["with-logind"\] = true/["with-logind"] = false/' "$BLUEZ_CFG"
    fi

    # Virtual loopback rig (see scripts/pipewire/tuiper-audio.conf):
    # tuiper-sink is the default playback target, and captures record its
    # monitor ports. Only for container audio (the `midi` profile's
    # tuiper-alsa service mounts /dev/snd and keeps normal routing).
    PW_USER_CONF=${XDG_CONFIG_HOME:-/root/.config}/pipewire/pipewire.conf.d
    mkdir -p "$PW_USER_CONF"
    if [ -f /workspace/scripts/pipewire/tuiper-audio.conf ]; then
        cp /workspace/scripts/pipewire/tuiper-audio.conf "$PW_USER_CONF/tuiper-audio.conf"
    fi

    pipewire >/tmp/pipewire.log 2>&1 &
    wireplumber >/tmp/wireplumber.log 2>&1 &

    # give the daemon a moment to publish its sockets
    ok=0
    if command -v pw-cli >/dev/null 2>&1; then
        for _ in $(seq 1 40); do
            if pw-cli info 0 >/dev/null 2>&1; then ok=1; break; fi
            sleep 0.25
        done
    fi
    if [ "$ok" = 1 ]; then
        echo "[entrypoint] pipewire session up (audio+MIDI)"
    else
        echo "[entrypoint] pipewire session check inconclusive; logs in /tmp/pipewire.log /tmp/wireplumber.log"
    fi

    # Route ALSA plugin clients: play to the null sink, capture its monitor.
    # WirePlumber 0.4's policy needs stream.capture.sink for that (see
    # scripts/pipewire/tuiper-audio.conf header). Skip with real hardware.
    if [ ! -d /dev/snd ]; then
        export PIPEWIRE_NODE=tuiper-sink
        export PIPEWIRE_ALSA="stream.capture.sink = true"
    fi
fi

# leading flag (e.g. -engine/-audio/-watch from `compose run tuiper ...`)
# means "run the tuiper binary with these flags"
if [ $# -gt 0 ] && [ "${1#-}" != "$1" ]; then
    set -- go run ./cmd/tuiper "$@"
fi

if [ $# -eq 0 ]; then
    exec bash
fi
exec "$@"
