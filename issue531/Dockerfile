# Bettbox#531 — systemd-resolved is not running.
#
# This is the Arch Linux default: NetworkManager owns /etc/resolv.conf, so nothing
# needs systemd-resolved and it stays disabled. The `resolvectl` binary is still
# installed, which is precisely why sing-tun's exec.LookPath("resolvectl") succeeds
# and the code proceeds to run three commands that all fail.
#
#   podman build -f issue531/Dockerfile -t sing-tun-resolved-repro-531 .
#   podman run --rm sing-tun-resolved-repro-531
#
# Nothing from the host is involved; the image is self-contained.

FROM docker.io/library/golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY issue531 ./issue531
RUN CGO_ENABLED=0 go build -trimpath -o /out/repro531 ./issue531

FROM docker.io/library/archlinux:latest

# systemd provides resolvectl. It is deliberately never started: see the mask below.
RUN pacman -Syu --noconfirm --needed systemd \
	&& rm -rf /var/cache/pacman/pkg/* /var/lib/pacman/sync/*

# resolved is installed but unavailable, deterministically.
#
# Masking the unit alone is not enough and gives a confusing message: D-Bus still
# finds the activation entry and tries to execute the masked unit, so resolvectl
# reports "Failed to execute program org.freedesktop.resolve1: Permission denied",
# which reads like a permissions problem rather than an absent resolver. Removing the
# D-Bus activation entry as well makes the cause unambiguous:
# "The name org.freedesktop.resolve1 was not provided by any .service files".
RUN ln -s /dev/null /etc/systemd/system/systemd-resolved.service \
	&& ln -s /dev/null /etc/systemd/system/org.freedesktop.resolve1.service \
	&& rm -f /usr/share/dbus-1/system-services/org.freedesktop.resolve1.service

COPY --from=build /out/repro531 /usr/local/bin/repro531

# Only the system bus is started. With no resolved behind it, every resolvectl call
# fails on connect - which is the condition the issue describes.
#
# dbus-daemon is detached with its output redirected, otherwise it keeps the
# container's stdout open and `podman run` never sees EOF.
ENTRYPOINT mkdir -p /run/dbus \
	&& rm -f /run/dbus/pid \
	&& dbus-daemon --system --fork >/dev/null 2>&1 \
	&& exec /usr/local/bin/repro531