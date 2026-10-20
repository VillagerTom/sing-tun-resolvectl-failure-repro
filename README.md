# sing-tun-resolved-repro

Reproductions and the proposed fix for a silent error-swallowing defect in
[`MetaCubeX/sing-tun`](https://github.com/MetaCubeX/sing-tun).

One defect, two triggers, two deliverables: a set of reproductions and a pull request
that turns the first red into green.

## The defect

`(*NativeTun).setSearchDomainForSystemdResolved` (`tun_linux.go:1048-1063`) hands the
system resolver's DNS configuration to `resolvectl` three times:

```go
go func() {
	_ = shell.Exec(ctlPath, "domain", t.options.Name, "~.").Run()
	_ = shell.Exec(ctlPath, "default-route", t.options.Name, "true").Run()
	_ = shell.Exec(ctlPath, append([]string{"dns", t.options.Name}, common.Map(dnsServer, netip.Addr.String)...)...).Run()
}()
```

The failure is swallowed twice over:

1. `_ =` discards the exit status of every command;
2. `shell.Exec` leaves `Cmd.Stderr` nil because `Attach()` is never called, so
   `resolvectl`'s explanation of *why* it failed goes to `/dev/null`.

`t.options.Logger` is never consulted, and the function returns `nil`, so `configure()`
(`tun_linux.go:409-414`) reports success and the tunnel comes up looking healthy while
DNS was never configured.

| | trigger | `resolvectl` says | reference |
| --- | --- | --- | --- |
| **#531** | `systemd-resolved` is not running (Arch default — NetworkManager owns `resolv.conf`) | `Failed to connect to service /run/systemd/resolve/io.systemd.Resolve: …` | [Bettbox#531](https://github.com/appshubcc/Bettbox/issues/531) |
| **#522** | `systemd-resolved` is running, but polkit denies because no authentication agent is registered yet | `Access denied as the requested operation requires interactive authentication. …` | [Bettbox#522](https://github.com/appshubcc/Bettbox/issues/522) |

## Layout

| path | what it is |
| --- | --- |
| `sing-tun/` | git submodule, pinned to `MetaCubeX/sing-tun@bfc9055` (`meta`, v0.4.27-5) — the revision the PR targets |
| `testdata/` | in-package tests that call the **real** unexported function; copied into `sing-tun/` by `make sync`, removed by `make clean` |
| `issue531/` | standalone repro program + Dockerfile — `systemd-resolved` absent |
| `issue522/` | standalone repro program + Dockerfile — polkit denies, no auth agent |
| `internal/resolvectlrepro/` | harness both call shapes go through, so "silent" and "witnessed" provably issue the identical argv |
| `patch/` | the proposed fix, as a git-applicable patch |
| `bugreport/` | the PR description, English and Chinese |

## Usage

```sh
git clone --recurse-submodules <url> sing-tun-resolved-repro
cd sing-tun-resolved-repro

make help
```

| target | what it does |
| --- | --- |
| `make test` | copies `testdata/` into the submodule and runs both repro tests. **Expected to FAIL** against the unfixed tree — that failure is the defect. |
| `make issue531` | builds and runs the `systemd-resolved`-absent container |
| `make issue522` | builds and runs the polkit-denied container (needs `--privileged`) |
| `make verify-fix` | applies `patch/`, re-runs `make test` — expected to **PASS** — then reverts |
| `make sync` / `make clean` | copy the fixtures into the submodule / take them back out |
| `make submodule` | `git submodule update --init --recursive` |

`ENGINE` and `IMAGE_PREFIX` are overridable, e.g. `make issue531 ENGINE=docker`.

## What each repro shows

All four artifacts assert the same thing in the same order, so none of them can pass
vacuously:

1. **the trigger fired** — with the exact argv sing-tun builds, including
   `dns Bettbox 198.18.0.2 fdfe:dcba:9876::2` as derived from the fake-ip range
   `198.18.0.0/15`;
2. **the commands really failed and the message existed** — replaying one with stderr
   captured returns the text quoted in the table above;
3. **the defect** — the logger saw nothing while three writes failed.

Step 2 deserves a note. Every `resolvectl` failure is a non-zero exit, so counting
failures proves nothing on its own: an unknown interface name fails before `resolvectl`
ever reaches systemd, and would satisfy a naive "3/3 failed" verdict while the intended
trigger never happened. Both standalone programs therefore match each failure against
the expected signature (`io.systemd.Resolve` / `org.freedesktop.resolve1` for #531,
`interactive authentication` / `Access denied` for #522) and refuse to claim a
reproduction otherwise:

```
$ ... repro522 -i NoSuchIfc0
NOT REPRODUCED: 3/3 writes failed, but not for the reason under test.
  resolvectl domain NoSuchIfc0 ~. failed for an unrelated reason, so the trigger did
  not fire: "Failed to resolve interface \"NoSuchIfc0\": No such device"
```

This is not hypothetical — it is exactly what the first version of the #522 image did,
because it never created the `Bettbox` link.

`issue531/main.go` and `issue522/main.go` run sing-tun's exact call shape and then
re-issue the identical commands with stderr captured, printing "what sing-tun sees" and
"what was thrown away" side by side:

```
--- (1) sing-tun's call shape: (*NativeTun).setSearchDomainForSystemdResolved ---
returned error: <nil>   <-- configure() calls this a success
log output:    <no log record at all>

--- (2) the same three commands, stderr captured ---
FAILED  resolvectl domain Bettbox ~.
        exit: exit status 1
        stderr: Failed to set domain configuration: ...
...
REPRODUCED: 3/3 resolvectl writes denied by polkit,
              and sing-tun reported success with no log output. resolved shows no
              DNS scope for the link, so name-based rules and fake-ip silently
              stopped applying.
```

Both programs exit `0` when the defect reproduced, `3` when their trigger is absent, and
`1` when it did not reproduce — never a false success.

## The fix

`patch/0001-log-resolvectl-failures-in-setSearchDomainForSystemdResolved.patch` keeps the
result of each command and reports it through `t.options.Logger`, using the same
`Read()` + `E.Extend` pattern the repository already uses at
`redirect_nftables_rules_openwrt.go:47-50`.

It logs and does **not** return an error: a non-nil return would abort `configure()` and
tear the tunnel down, which would turn a silent partial failure into a hard one for
users who never asked for systemd-resolved. See `bugreport/PR.en.md` for the reasoning.

`make verify-fix` demonstrates the transition:

```
before:  --- FAIL: TestReproIssue531ResolvedAbsent
after:   --- PASS: TestReproIssue531ResolvedAbsent
```

## Notes on the environments

- **#531 reproduces on any host where `systemd-resolved` is absent** — the Arch Linux
  default. The exact wording of `resolvectl`'s failure depends on how it is absent:
  masked gives `Could not activate remote peer …`, not activatable at all gives
  `The name org.freedesktop.resolve1 was not provided by any .service files`, and a
  merely disabled unit with a stale socket gives `Failed to connect to service
  /run/systemd/resolve/io.systemd.Resolve: …`. All three are the same condition, and
  the harness accepts any of them. The verbatim message quoted in the issue is used by
  the deterministic stub in `testdata/`.
- **#522 needs the real polkit denial**, which cannot be staged with a stub: the check
  happens inside `resolvectl`, against the real `org.freedesktop.resolve1.policy`
  actions. The Dockerfile starts `dbus-daemon`, `polkitd` and `systemd-resolved` by hand
  — no systemd as PID 1 is needed — creates no authentication agent, creates the
  `Bettbox` link that `resolvectl` insists on resolving first, and runs the repro as
  uid 1000. `--privileged` is required both because `systemd-resolved` drops privileges
  to the `systemd-resolve` user and because the tap device needs `CAP_NET_ADMIN`.
- The #522 image also prints the issue's control case before dropping privileges: the
  same three commands **succeed** as root, because `sd_bus_query_sender_privilege()`
  reports the sender as privileged and polkit is never consulted. Only a genuinely
  privileged sender skips polkit, which is why a client with `cap_net_admin` breaks
  where a setuid build does not.

## Environment requirement for `podman build`

`podman build` needs an overlay filesystem: buildah mounts the build context through
overlay regardless of which storage driver is in use. Verify with:

```sh
grep overlay /proc/filesystems
```

This must print `nodev  overlay`. If it prints nothing, the filesystem is not available,
and every build fails with

```
Error: mounting an overlay over build context directory: creating overlay scaffolding
for build context directory: mount overlay:...: no such device
```

Note the failure is `no such device`, not `permission denied` — no amount of `--privileged`,
`--storage-driver vfs`, a different `TMPDIR`, or `--isolation=chroot` helps, because the
overlay is not the storage driver. It is usually just an unloaded module:

```sh
modinfo -F filename overlay   # /lib/modules/$(uname -r)/kernel/fs/overlayfs/overlay.ko.zst
sudo modprobe overlay
grep overlay /proc/filesystems   # nodev  overlay
```

If the module is genuinely absent (a custom kernel built without `CONFIG_OVERLAY_FS`), the
remaining option is `fuse-overlayfs`, which implements overlay in userspace and needs a
separate storage root (`podman --storage-driver fuse-overlayfs`).

`podman run` never needs any of this — it uses the storage driver, not an overlay over the
build context — so a host that cannot build can still run a previously built image.

## Relationship to `SagerNet/sing-tun`

`sing-tun` exists as two sibling modules that share an ancestor (this one's README still
carries `nekohasekai <contact-sagernet@sekai.icu>`), so the defect was inherited rather
than introduced twice:

| module | consumer | issues | teardown path |
| --- | --- | --- | --- |
| `MetaCubeX/sing-tun` (this submodule) | mihomo, and the GUI clients built on it | **disabled** | logs at Debug, added by [PR #9](https://github.com/MetaCubeX/sing-tun/pull/9) |
| `SagerNet/sing-tun` | sing-box | enabled | **no logging at all** |

They are patched independently, so a fix on one side does not propagate. Both field
reports came in through the metacubex side, which is what this repository targets; the
sibling module needs its own patch.