# tuiper development image
#
# Linux + CGO toolchain (RtMidi needs gcc/g++ and Alsa headers) + Go with its
# caches in named volumes so rebuilds stay fast between container runs.
#
# TUI development happens here with the `test` engine (loopback ports, no
# hardware). Real MIDI is a host-side concern and stays that way on Windows:
# Docker Desktop can't expose WinMM MIDI endpoints into a container. On a
# Linux host, the optional `midi` compose profile mounts /dev/snd for a real
# ALSA path.
FROM golang:1.27-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        gcc g++ pkg-config libasound2-dev \
    && rm -rf /var/lib/apt/lists/* \
    && git config --global --add safe.directory /workspace

ENV CGO_ENABLED=1 CC=gcc CXX=g++

WORKDIR /workspace
