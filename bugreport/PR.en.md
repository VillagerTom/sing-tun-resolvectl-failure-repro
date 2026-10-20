# Log `resolvectl` failures in `setSearchDomainForSystemdResolved` instead of discarding them

## Summary

`(*NativeTun).setSearchDomainForSystemdResolved` configures systemd-resolved with three
`resolvectl` commands. Every failure of those commands is discarded, twice over:

1. the exit status is dropped (`_ = ...Run()`), and
2. `resolvectl`'s stderr never reaches anyone, because `shell.Exec` is used without
   `Attach()`, leaving the child's `Stderr` nil.

The function then returns `nil`, so `configure()` reports success. When the writes do
fail, the TUN comes up, the caller believes DNS was handed over, and nothing anywhere
records that it was not. The result is that the whole `dns-hijack` path silently stops
working, with no diagnostic trail to point at.

This patch keeps the result instead of throwing it away and reports it through
`t.options.Logger`. It is deliberately **logging only** — see
[Why this does not return an error](#why-this-does-not-return-an-error).

## The asymmetry was already reported

[PR #9](https://github.com/MetaCubeX/sing-tun/pull/9) (merged 2026-02-13) opened with:

> Previously, the initialization and teardown procedures were **asymmetrical**, causing
> DNS configurations to persist in `systemd-resolved` after the interface was …

That PR added `unsetSearchDomainForSystemdResolved`, wired it into `Close`, and — in the
same commit — gave it a `t.options.Logger.Debug(...)` line. It did not touch
`setSearchDomainForSystemdResolved`, whose three `_ = ...Run()` calls were, and still
are, one function away in the same file.

So the write path stayed invisible while the teardown path became visible, and the
question "did this actually configure anything?" now has an answer for one direction
only. This patch closes that half. It also brings the sibling
`SagerNet/sing-tun` module, where neither direction logs, in line with the Debug line
that already exists here.

## Root cause

`tun_linux.go:1048-1063`:

```go
func (t *NativeTun) setSearchDomainForSystemdResolved() error {
	ctlPath, err := exec.LookPath("resolvectl")
	if err != nil {
		return nil
	}
	dnsServer, err := t.options.DNSServerAddress()
	if err != nil {
		return err
	}
	go func() {
		_ = shell.Exec(ctlPath, "domain", t.options.Name, "~.").Run()                                        // :1058
		_ = shell.Exec(ctlPath, "default-route", t.options.Name, "true").Run()                               // :1059
		_ = shell.Exec(ctlPath, append([]string{"dns", t.options.Name}, common.Map(dnsServer, netip.Addr.String)...)...).Run() // :1060
	}()
	return nil
}
```

**The exit status is discarded.** `_ =` on all three calls. Nothing in the function body
can observe a failure, and because the work runs in a detached goroutine the return
value could not carry one even if it were checked afterwards.

**The diagnostic is discarded.** In `github.com/metacubex/sing/common/shell`, `Exec`
builds a bare `exec.Cmd` and `Attach` is what sets `Stdin`/`Stdout`/`Stderr`. `Attach`
is never called here, so `Stderr` stays nil and `os/exec` wires the child's stderr to
`/dev/null`. `resolvectl` does print an explanation of exactly what went wrong — it is
written to `/dev/null`.

**The logger is never consulted.** `t.options.Logger` is not read anywhere in this
function.

The correct pattern is already used elsewhere in this repository with the same `shell`
package — `redirect_nftables_rules_openwrt.go:47-50`:

```go
output, err := shell.Exec(fw4Path, "reload").Read()
if err != nil {
	return E.Extend(E.Cause(err, "reload fw4 rules"), output)
}
```

The same three functions now run in a loop that does exactly this.

## Trigger A — `systemd-resolved` is not running

This is the Arch Linux default: NetworkManager owns `/etc/resolv.conf`, so nothing
needs `systemd-resolved` and it stays disabled. The `resolvectl` **binary** is still
installed, so `exec.LookPath` succeeds and sing-tun proceeds normally — and then every
D-Bus call fails on connect.

Verified on a host in exactly that state (`systemd-resolved` disabled,
`/run/systemd/resolve/` absent, `/etc/resolv.conf` a plain NetworkManager-written file
holding the DHCP router address):

```
$ resolvectl status
Failed to connect to service /run/systemd/resolve/io.systemd.Resolve: No such file or directory
```

The three writes, with stderr captured — this is what the current code throws away:

```
resolvectl domain Bettbox ~.                     exit status 1
    stderr: Failed to set domain configuration: ...
resolvectl default-route Bettbox true            exit status 1
    stderr: Failed to set default route configuration: ...
resolvectl dns Bettbox 198.18.0.2 fdfe:dcba:9876::2   exit status 1
    stderr: Failed to set DNS configuration: ...
```

What the caller sees instead: a `nil` error and no log output.

This trigger is permanent — retrying never helps — which makes the absence of any
message the most costly case: there is no later symptom that points back at the moment
the DNS configuration was lost.

## Trigger B — polkit denies, no authentication agent registered yet

Here `systemd-resolved` is running and reachable, so the D-Bus call gets as far as the
authorization check. The four write actions (`set-domains`, `set-dns-servers`,
`set-default-route`, `revert`) are declared `auth_admin` / `auth_admin_keep` in
`/usr/share/polkit-1/actions/org.freedesktop.resolve1.policy`. `resolvectl` allows
interactive authorization by default, so it is able to prompt — but when a client starts
at login, the desktop authentication agent has not registered with polkitd yet, so
polkit has nowhere to prompt.

Reproduced end to end in a container (`dbus-daemon --system` + `polkitd` +
`systemd-resolved` started by hand, no agent, caller unprivileged):

```
caller: uid=1000 (unprivileged, so polkit is consulted)
precondition ok: systemd-resolved is reachable
```

Control case first, same container, same three commands as root — all succeed, because
`sd_bus_query_sender_privilege()` reports the caller as privileged and polkit is never
consulted. Recent `resolvectl` no longer prints a message on success, so the state is the
evidence:

```
=== control case from the issue: the same three commands as root ===
  domain        ok
  default-route ok
  dns           ok
    Current Scopes: none
       DNS Servers: 198.18.0.2 fdfe:dcba:9876::2
        DNS Domain: ~.
     Default Route: yes
=== reverted, now as the unprivileged caller with no agent registered ===
  revert        ok
    Current Scopes: none
     Default Route: no
```

Then as an unprivileged user with no agent registered:

```
resolvectl domain Bettbox ~.
    exit: exit status 1
    stderr: Failed to set domain configuration: Access denied as the requested operation
            requires interactive authentication. However, interactive authentication has
            not been enabled by the calling program.
resolvectl default-route Bettbox true    -> same
resolvectl dns Bettbox 198.18.0.2 ...    -> same
```

Resulting state — nothing was configured:

```
$ resolvectl status Bettbox
Link 3 (Bettbox)
    Current Scopes: none
         Protocols: -DefaultRoute +LLMNR +mDNS -DNSOverTLS DNSSEC=no/unsupported
     Default Route: no
```

And polkitd's own log is empty: it runs at `--log-level=notice` and only records
authentication *failures*, so a request it could not prompt for leaves no trace either.
Between the discarded exit code, the discarded stderr and the absent polkit record, this
failure leaves no trace at all.

This trigger is transient and policy-dependent, which makes it worse rather than better:
the same binary on the same machine succeeds a moment later, so there is nothing for a
user to correlate against.

Note that file capabilities do not help here — only a genuinely privileged sender skips
polkit — so a client running with `cap_net_admin` is exactly the configuration that
breaks while a setuid build does not.

## Impact

DNS takeover depends on this being the only path that hands the system resolver to the
TUN's own DNS server. When it fails, local applications resolve real public addresses
instead of the TUN's DNS server, so:

- name-based rules no longer match and traffic silently takes the wrong route;
- `hosts` injection is bypassed;
- anything that needs a proxy to reach its DNS fails outright, with an error that points
  at the network rather than at DNS configuration.

From the application's point of view, "TUN enabled" and "DNS is being handled" become
indistinguishable states.

## The change

```diff
 	go func() {
-		_ = shell.Exec(ctlPath, "domain", t.options.Name, "~.").Run()
-		_ = shell.Exec(ctlPath, "default-route", t.options.Name, "true").Run()
-		_ = shell.Exec(ctlPath, append([]string{"dns", t.options.Name}, common.Map(dnsServer, netip.Addr.String)...)...).Run()
+		commands := [][]string{
+			{"domain", t.options.Name, "~."},
+			{"default-route", t.options.Name, "true"},
+			append([]string{"dns", t.options.Name}, common.Map(dnsServer, netip.Addr.String)...),
+		}
+		for _, args := range commands {
+			output, err := shell.Exec(ctlPath, args...).Read()
+			if err != nil && t.options.Logger != nil {
+				t.options.Logger.Error(E.Extend(E.Cause(err, "set DNS configuration for interface: ", t.options.Name), output))
+			}
+		}
 	}()
```

One function, no behaviour change on the success path: the same three commands, in the
same order, with the same argv. The only difference is that a failure is now recorded.

## Why this does not return an error

It would be tempting to propagate the failure instead of logging it. That would be a
regression:

`configure()` (`tun_linux.go:409-414`) aborts and tears the TUN down on a non-nil return
from `setSearchDomainForSystemdResolved`. Machines that legitimately run without
`systemd-resolved` are common — Trigger A is the Arch Linux default — so returning an
error would convert a silent partial failure into a hard failure, taking the tunnel
down for users who never asked for systemd-resolved in the first place.

The work also runs in a detached goroutine, so the return value cannot carry the result
anyway without making TUN startup synchronous.

Logging is the safe half of this fix, and it is the half that was missing.

## Scope

This PR makes the failure **observable**. It does not make DNS work in either trigger:

- Trigger A needs a fallback for the "resolved is absent" case (own the stub, or add a
  config-shape change at the caller — the sibling module already ships an optional
  service that claims `org.freedesktop.resolve1` for exactly this reason).
- Trigger B is not retryable from here. Whether the polkit failure clears depends on when
  the desktop agent registers, and a process cannot observe or query that: polkit's
  `Authority` interface exposes only agent-side methods, nothing to ask. A caller that
  wants certainty has to poll `resolvectl status <if>` for `DNS Domain: ~.` after the
  tunnel is up.

Both belong to the caller, and both are out of scope here. What is in scope is that a
user hitting either of them gets a line in the log instead of a tunnel that looks fine
and resolves nothing.

One optional refinement, deliberately left out to keep the diff minimal: the message
could distinguish "systemd-resolved is not running" (a configuration the user chose,
worth one line at Info) from "polkit denied the request" (transient, worth Error). A
cheap up-front probe of `/run/systemd/resolve/io.systemd.Resolve` would allow that.

## Reproduction

Two independent triggers, both verified against this tree. Reproduction lives in
`VillagerTom/sing-tun-resolved-repro`, with `sing-tun` pinned as a submodule:

```
git clone --recurse-submodules https://github.com/VillagerTom/sing-tun-resolved-repro
cd sing-tun-resolved-repro

make test        # both triggers, deterministic, no root, no container
make issue531    # trigger A, against the real resolvectl, in a container
make issue522    # trigger B, in a container (needs --privileged)
```

`make test` copies `tests/*.go` into the submodule so they can reach the unexported
function, then runs them. The tests call the **real**
`setSearchDomainForSystemdResolved` with a `resolvectl` stand-in on `PATH`, and assert in
order:

1. the trigger fired, with the exact argv sing-tun builds —
   `domain Bettbox ~.`, `default-route Bettbox true`,
   `dns Bettbox 198.18.0.2 fdfe:dcba:9876::2`;
2. the commands really failed and the message really exists — replaying one with stderr
   captured returns the text quoted above;
3. the logger saw something.

Step 3 is what fails today, and it cannot pass vacuously because steps 1 and 2 run
first. With this patch applied, both tests pass:

```
--- PASS: TestReproIssue522PolkitAgentNotReady (0.03s)
--- PASS: TestReproIssue531ResolvedAbsent (0.01s)
ok  	github.com/metacubex/sing-tun	0.048s
```

and the recorded line is the information that was previously discarded:

```
error: set DNS configuration for interface: Bettbox: execute (/usr/bin/resolvectl) … :
       exit status 1: Failed to set domain configuration: Access denied as the requested
       operation requires interactive authentication. However, interactive authentication
       has not been enabled by the calling program.
```

The patch is also attached as `patch/0001-…patch`; `git apply` it and re-run
`make test` to see the same transition.

## The same defect in `SagerNet/sing-tun`

The sibling module has the identical three `_ = ...Run()` calls in the identical
goroutine, and its `github.com/sagernet/sing/common/shell` is byte-identical to this
one. Neither direction logs there. It is what sing-box consumes, so sing-box users hit
both triggers too; a fix here does not propagate, and that module will need its own.

## Downstream reports

Both field reports that surfaced this came in through a GUI client built on a mihomo
core, i.e. through this module:

- https://github.com/appshubcc/Bettbox/issues/531 — `systemd-resolved` not running (Arch
  default), all three writes fail, DNS takeover silently absent.
- https://github.com/appshubcc/Bettbox/issues/522 — polkit denies the writes because no
  authentication agent is registered yet, same outcome, and the same commands succeed
  one minute later.

## Note on delivery

Issues are disabled on this repository, so this is submitted as a pull request rather
than an issue report.