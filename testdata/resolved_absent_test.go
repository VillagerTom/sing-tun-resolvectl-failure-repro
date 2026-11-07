//go:build linux

// Repro: systemd-resolved is not running.
//
// Trigger: systemd-resolved is not installed/enabled. This is the Arch Linux default -
// NetworkManager owns resolv.conf, so `systemctl enable systemd-resolved` is
// never needed and never happens. The `resolvectl` binary itself may well be
// present, so `exec.LookPath` succeeds and sing-tun proceeds, but every D-Bus call
// dies on connect.
//
// This file is not part of sing-tun. It is copied into the tree by `make sync` and
// removed again by `make clean`; see ../Makefile. For the real trigger, with no
// stub at all, use the container image built from ../resolved-absent/Dockerfile.
//
// # WHAT THIS TEST PROVES
//
// The three writes all fail, resolvectl explains why on stderr, and sing-tun
// discards both the exit status and the explanation. Nothing reaches the logger,
// so the caller has no way to learn that DNS hijacking did not happen. Worse, the
// return value is nil, so (*NativeTun).configure reports success and the tunnel
// comes up looking healthy.
//
// EXPECTED RESULT AGAINST CURRENT sing-tun: FAIL on the last assertion.
//
// A passing run means the fix landed: the failure is logged (or returned) instead
// of dropped.
package tun

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReproResolvedAbsent(t *testing.T) {
	// Verbatim stderr resolvectl produces when it cannot reach
	// /run/systemd/resolve/io.systemd.Resolve (Arch Linux default state).
	const resolvectlStderr = "Failed to connect to service " +
		"/run/systemd/resolve/io.systemd.Resolve: No such file or directory\n"

	const interfaceName = "tun0"
	invocationLog := reproStubResolvectl(t, resolvectlStderr)
	recorder := newReproRecordingLogger()

	nativeTun := &NativeTun{options: reproOptions(interfaceName, recorder)}

	// ---------------------------------------------------------------------
	// Step 1: the real sing-tun code path.
	// ---------------------------------------------------------------------
	err := nativeTun.setSearchDomainForSystemdResolved()
	require.NoError(t, err,
		"setSearchDomainForSystemdResolved returns nil, so configure() reports success")

	// Non-vacuity: prove the trigger really fired, with the exact argv sing-tun
	// builds, before asserting anything about observability.
	invocations := reproWaitInvocations(t, invocationLog, 3)
	require.Equal(t, []string{
		"resolvectl domain " + interfaceName + " ~.",
		"resolvectl default-route " + interfaceName + " true",
		"resolvectl dns " + interfaceName + " 198.18.0.2 fdfe:dcba:9876::2",
	}, invocations)

	// ---------------------------------------------------------------------
	// Step 2: the information sing-tun threw away.
	// ---------------------------------------------------------------------
	// Attach() is never called, so the child's Stderr is nil and the message
	// below went to /dev/null. Replay one command with stderr captured to show
	// the error was real and available.
	captured, captureErr := reproWitness("domain", interfaceName, "~.")
	require.Error(t, captureErr, "resolvectl genuinely failed")
	require.Contains(t, captured, "Failed to connect to service",
		"resolvectl did say something useful - sing-tun just never looked")

	// ---------------------------------------------------------------------
	// Step 3: what the user is left with.
	// ---------------------------------------------------------------------
	t.Logf("resolvectl exit error sing-tun discarded: %v", captureErr)
	t.Logf("resolvectl stderr sing-tun discarded:     %q", captured)
	t.Logf("sing-tun logger output:                   %s", recorder)

	// The defect: three failed writes, zero records emitted. DNS was never
	// configured for this interface, yet nothing says so.
	require.NotEmpty(t, recorder.Records(),
		"resolved-absent: all three resolvectl writes failed, "+
			"setSearchDomainForSystemdResolved returned nil, and nothing was logged - "+
			"the DNS takeover failed silently")
}
