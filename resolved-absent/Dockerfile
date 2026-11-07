# systemd-resolved is not running.
#
# This is the Arch Linux default: NetworkManager owns /etc/resolv.conf, so nothing
# needs systemd-resolved and it stays disabled. The `resolvectl` binary is still
# installed, which is precisely why sing-tun's exec.LookPath("resolvectl") succeeds
# and the code proceeds to run three commands that all fail.
#
#   podman build -f resolved-absent/Dockerfile -t sing-tun-resolvectl-failure-repro-resolved-absent .
#   podman run --rm sing-tun-resolvectl-failure-repro-resolved-absent
#
# Nothing from the host is involved; the image is self-contained.

FROM docker.io/library/golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY resolved-absent ./resolved-absent
RUN CGO_ENABLED=0 go build -trimpath -o /out/repro ./resolved-absent

FROM docker.io/library/archlinux:latest

# systemd provides resolvectl, the binary sing-tun looks for through PATH. The daemon is
# installed but deliberately never started.
RUN pacman -Syu --noconfirm --needed systemd \
	&& rm -rf /var/cache/pacman/pkg/* /var/lib/pacman/sync/*

# resolved is installed but unreachable, deterministically.
#
# Nothing here runs systemd as PID 1, so masking the unit would change nothing: resolvectl
# talks to the system bus, not to systemd, and the bus is what starts resolved on demand.
# That path is the activation entry below. Left in place it produced
# "Failed to execute program org.freedesktop.resolve1: Permission denied", which reads like
# a permissions problem rather than an absent resolver. Removing it makes the cause
# unambiguous: "The name org.freedesktop.resolve1 was not provided by any .service files".
RUN rm -f /usr/share/dbus-1/system-services/org.freedesktop.resolve1.service

COPY --from=build /out/repro /usr/local/bin/repro

# Only the system bus is started. With no resolved behind it, every resolvectl call
# fails on connect - which is the condition under test.
#
# dbus-daemon is detached with its output redirected, otherwise it keeps the
# container's stdout open and `podman run` never sees EOF.
ENTRYPOINT mkdir -p /run/dbus \
	&& rm -f /run/dbus/pid \
	&& dbus-daemon --system --fork >/dev/null 2>&1 \
	&& exec /usr/local/bin/repro
