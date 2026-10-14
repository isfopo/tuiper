# tuiper development image
#
# Linux + CGO toolchain (RtMidi needs gcc/g++ and Alsa headers) + Go with its
# caches in named volumes so rebuilds stay fast between container runs.
#
# PipeWire is installed as an in-container audio+MIDI session: the committed
# scripts/container-entrypoint.sh starts a private daemon (before the
# compose services' command), so audio and MIDI code gets real ALSA devices
# sink/monitor capture with no host hardware. See docker-compose.yml for the
# entrypoint wiring, and the -audio/-loopback flags of tuiper for the
# milestone-2 audio I/O checks.
FROM golang:1.27-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        gcc g++ pkg-config libasound2-dev \
        pipewire pipewire-pulse pipewire-alsa pipewire-bin wireplumber \
        alsa-utils dbus \
    && rm -rf /var/lib/apt/lists/* \
    && git config --global --add safe.directory /workspace

ENV CGO_ENABLED=1 CC=gcc CXX=g++

WORKDIR /workspace
