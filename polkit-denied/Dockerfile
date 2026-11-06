# polkit denies the writes because no authentication agent is registered yet.
#
# The trigger is environmental, not code: systemd-resolved reachable, polkit
# reachable, caller unprivileged, and no agent able to prompt. Getting that on a
# desktop means logging out and back in with a client set to autostart. This image
# reproduces it instead, so the host's DNS configuration is never touched.
#
#   podman build -f polkit-denied/Dockerfile -t sing-tun-resolved-repro-polkit-denied .
#   podman run --rm --privileged sing-tun-resolved-repro-polkit-denied
#
# --privileged is required: systemd-resolved drops privileges to the systemd-resolve
# user, and the repro needs CAP_NET_ADMIN below to create the link resolvectl is
# pointed at.

FROM docker.io/library/golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY polkit-denied ./polkit-denied
RUN CGO_ENABLED=0 go build -trimpath -o /out/repro ./polkit-denied

FROM docker.io/library/archlinux:latest

RUN pacman -Syu --noconfirm --needed systemd polkit \
	&& rm -rf /var/cache/pacman/pkg/* /var/lib/pacman/sync/*

COPY --from=build /out/repro /usr/local/bin/repro

# uid 1000 is the unprivileged caller. Polkit is skipped for EUID 0, so the repro
# must not be root - and neither may a client built with file capabilities only.
RUN useradd --create-home --uid 1000 repro \
	&& chmod 755 /usr/local/bin/repro

# Only these three daemons are needed; no systemd has to be PID 1. The polkit check
# that rejects the call happens client-side inside resolvectl, so resolved not being
# managed by systemd is irrelevant.
#
#   1. system bus
#   2. polkitd      - publishes the org.freedesktop.resolve1.* actions from
#                     /usr/share/polkit-1/actions/org.freedesktop.resolve1.policy
#   3. systemd-resolved
#
# No polkit agent is ever started, which is the whole point: polkit has nowhere to
# prompt and denies the request.
#
# The link has to exist before the repro runs: resolvectl turns the interface name
# into an ifindex first and fails with "No such device" otherwise, which would abort
# before the polkit check is ever reached. It is created here rather than in a RUN
# because creating a tap device needs CAP_NET_ADMIN, which a build cannot grant.
#
# polkitd and resolved are backgrounded (they never exit) and their output is
# redirected, otherwise the first one blocks the chain and the second holds the
# container's stdout open so `podman run` never sees EOF.
ENTRYPOINT mkdir -p /run/dbus \
	&& rm -f /run/dbus/pid \
	&& dbus-daemon --system --fork >/dev/null 2>&1 \
	&& sleep 1 \
	&& /usr/lib/polkit-1/polkitd --no-debug --log-level=notice >/var/log/polkitd.log 2>&1 & \
	sleep 1; \
	/usr/lib/systemd/systemd-resolved >/var/log/resolved.log 2>&1 & \
	sleep 2; \
	ip tuntap add dev tun0 mode tap; \
	ip link set tun0 up; \
	echo "=== control: the same three commands as root ==="; \
	resolvectl domain tun0 "~." && echo "  domain        ok" || echo "  domain        FAILED"; \
	resolvectl default-route tun0 true && echo "  default-route ok" || echo "  default-route FAILED"; \
	resolvectl dns tun0 198.18.0.2 fdfe:dcba:9876::2 && echo "  dns           ok" || echo "  dns           FAILED"; \
	resolvectl status tun0 | grep -E 'Current Scopes|DNS Servers|DNS Domain|Default Route'; \
	echo "=== reverted, now as the unprivileged caller with no agent registered ==="; \
	resolvectl revert tun0 && echo "  revert        ok" || echo "  revert        FAILED"; \
	resolvectl status tun0 | grep -E 'Current Scopes|DNS Servers|DNS Domain|Default Route'; \
	exec setpriv --reuid=1000 --regid=1000 --init-groups /usr/local/bin/repro
