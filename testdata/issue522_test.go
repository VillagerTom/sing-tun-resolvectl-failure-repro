//go:build linux

// Repro for https://github.com/appshubcc/Bettbox/issues/522
//
// Trigger: systemd-resolved IS running and reachable, so the D-Bus call gets as
// far as the polkit check. When Bettbox starts at login the desktop
// authentication agent has not registered with polkitd yet, so polkit has no way
// to prompt. resolvectl returns "Access denied as the requested operation requires
// interactive authentication", and sing-tun discards it.
//
// This file is not part of sing-tun. It is copied into the tree by `make sync` and
// removed again by `make clean`; see ../Makefile. For the real trigger, with no
// stub at all, use the container image built from ../issue522/Dockerfile.
//
// This failure is the worst kind: it is transient and policy-dependent, so the
// same binary on the same machine succeeds a minute later. Nothing in the logs
// hints that the earlier attempt ever happened, and resolved reports the interface
// as having no DNS scope and no default route.
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

func TestReproIssue522PolkitAgentNotReady(t *testing.T) {
	// Verbatim stderr the reporter captured from resolvectl during a
	// login-autostart run (issue #522).
	const resolvectlStderr = "Failed to set domain configuration: Access denied as the " +
		"requested operation requires interactive authentication. However, interactive " +
		"authentication has not been enabled by the calling program.\n" +
		"Failed to set default route configuration: Access denied as the requested operation " +
		"requires interactive authentication. However, interactive authentication has not " +
		"been enabled by the calling program.\n" +
		"Failed to set DNS configuration: Access denied as the requested operation requires " +
		"interactive authentication. However, interactive authentication has not been " +
		"enabled by the calling program.\n"

	const interfaceName = "Bettbox"
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
	// `unsetSearchDomainForSystemdResolved` - the teardown half - does log at
	// Debug level, which is why the failure is asymmetric: the code that
	// configures is silent, the code that reverts is not.
	captured, captureErr := reproWitness("domain", interfaceName, "~.")
	require.Error(t, captureErr, "polkit genuinely denied the call")
	require.Contains(t, captured, "requires interactive authentication",
		"resolvectl did explain the denial - sing-tun just never looked")

	// ---------------------------------------------------------------------
	// Step 3: what the user is left with.
	// ---------------------------------------------------------------------
	t.Logf("resolvectl exit error sing-tun discarded: %v", captureErr)
	t.Logf("resolvectl stderr sing-tun discarded:     %q", captured)
	t.Logf("sing-tun logger output:                   %s", recorder)
	t.Logf("note: the teardown half, unsetSearchDomainForSystemdResolved, is equally " +
		"unobservable - how much of this is reported depends on the tree")

	// The defect: three denied writes, zero records emitted. resolved reports the
	// interface with no DNS scope, yet nothing says so.
	require.NotEmpty(t, recorder.Records(),
		"Bettbox#522: polkit denied all three resolvectl writes, setSearchDomainForSystemdResolved "+
			"returned nil, and nothing was logged - DNS hijacking was never configured")
}
