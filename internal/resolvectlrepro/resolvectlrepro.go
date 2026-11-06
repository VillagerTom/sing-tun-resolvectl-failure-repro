// Package resolvectlrepro drives the `resolvectl` writes that sing-tun performs for
// a TUN device, in the two shapes that matter for the two repros in this project.
//
// The defective shape is a verbatim copy of the body of
// (*NativeTun).setSearchDomainForSystemdResolved in MetaCubeX/sing-tun
// (tun_linux.go:1048-1063):
//
//	go func() {
//		_ = shell.Exec(ctlPath, "domain", t.options.Name, "~.").Run()
//		_ = shell.Exec(ctlPath, "default-route", t.options.Name, "true").Run()
//		_ = shell.Exec(ctlPath, append([]string{"dns", t.options.Name}, servers...)...).Run()
//	}()
//
// `_ =` drops the exit status, and because Attach() is never called the child's
// Stderr is nil, so resolvectl's explanation goes to /dev/null. Nothing reaches
// t.options.Logger, and the function returns nil, so the tunnel comes up looking
// healthy while DNS was never configured.
//
// The same defect has two independent triggers, which is why there are two repros:
//
//	resolved-unreachable  systemd-resolved is not running, so every D-Bus call dies
//	                      -> "Failed to connect to service /run/systemd/resolve/io.systemd.Resolve: ..."
//	polkit-denied         systemd-resolved is reachable, but polkit refuses because no
//	                      authentication agent is registered yet
//	                      -> "Access denied as the requested operation requires interactive authentication..."
//
// Witness runs the identical argv with stderr captured, so a repro can show what
// the silent shape threw away.
package resolvectlrepro

import (
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"

	"github.com/metacubex/sing/common/shell"
)

// Signatures identifies the intended failure, for CheckTrigger. Resolvectl's prose is
// localised, but these identifiers survive translation.
var (
	// ResolvedAbsent: every variant resolvectl uses when it cannot reach
	// systemd-resolved, whether or not D-Bus activation is consulted.
	ResolvedAbsent = []string{"io.systemd.Resolve", "org.freedesktop.resolve1"}
	// PolkitDenied: the interactive-authorization denial behind the polkit-denied repro.
	PolkitDenied = []string{"interactive authentication", "Access denied"}
)

// DefaultInterface is the TUN device name the repros configure. sing-tun passes the
// configured name through verbatim, so any name works; this is just a conventional one.
const DefaultInterface = "tun0"

// FakeIPPrefix4 and FakeIPPrefix6 are the TUN addresses for the default fake-ip
// range 198.18.0.0/15.
var (
	FakeIPPrefix4 = netip.MustParsePrefix("198.18.0.1/30")
	FakeIPPrefix6 = netip.MustParsePrefix("fdfe:dcba:9876::1/126")
)

// DNSServers returns the addresses sing-tun publishes into systemd-resolved for a
// TUN with the given addresses.
//
// This mirrors Options.DNSServerAddress() rather than calling it: this module
// deliberately does not depend on sing-tun, so that the container images stay small
// (importing it would drag in mipstack and gvisor). The values are pinned from the
// other side instead — testdata/resolved_unreachable_test.go asserts that the real
// Options.DNSServerAddress() produces exactly this argv, including
// `dns tun0 198.18.0.2 fdfe:dcba:9876::2` — so any drift is caught there.
func DNSServers(inet4Address, inet6Address []netip.Prefix) []string {
	servers := make([]string, 0, len(inet4Address)+len(inet6Address))
	if len(inet4Address) > 0 {
		if next, ok := nextAddress(inet4Address[0]); ok {
			servers = append(servers, next.String())
		}
	}
	if len(inet6Address) > 0 {
		if next, ok := nextAddress(inet6Address[0]); ok {
			servers = append(servers, next.String())
		}
	}
	return servers
}

// nextAddress is Options.DNSServerAddress()'s rule: the address right after the
// interface address, provided it stays inside the prefix (HasNextAddress(addr, 1)).
func nextAddress(prefix netip.Prefix) (netip.Addr, bool) {
	if !prefix.IsValid() || !prefix.Addr().IsValid() {
		return netip.Addr{}, false
	}
	next := prefix.Addr().Next()
	if !next.IsValid() || !prefix.Contains(next) {
		return netip.Addr{}, false
	}
	return next, true
}

// Commands returns the argv of the three writes, in the order sing-tun issues them.
// Both Silent and Witness build on this, so the two shapes are provably identical.
func Commands(interfaceName string, dnsServers []string) [][]string {
	dns := append([]string{"dns", interfaceName}, dnsServers...)
	return [][]string{
		{"domain", interfaceName, "~."},
		{"default-route", interfaceName, "true"},
		dns,
	}
}

// Silent replays (*NativeTun).setSearchDomainForSystemdResolved: detached
// goroutine, every exit status discarded, no stderr attached, no logging. The only
// value it can ever hand back is nil, which is why configure() cannot tell a
// working DNS takeover from a completely dead one.
func Silent(interfaceName string, dnsServers []string) error {
	ctlPath, err := exec.LookPath("resolvectl")
	if err != nil {
		return nil
	}
	go func() {
		for _, args := range Commands(interfaceName, dnsServers) {
			_ = shell.Exec(ctlPath, args...).Run()
		}
	}()
	return nil
}

// Attempt is one `resolvectl` invocation as sing-tun throws it away.
type Attempt struct {
	Args   []string
	Err    error
	Stderr string
}

func (a Attempt) String() string {
	command := "resolvectl " + strings.Join(a.Args, " ")
	if a.Err == nil {
		return "ok      " + command
	}
	// a.Err is sing's E.Cause wrapping, whose message already repeats the argv.
	var exitErr *exec.ExitError
	status := a.Err.Error()
	if errors.As(a.Err, &exitErr) {
		status = exitErr.Error()
	}
	report := "FAILED  " + command + "\n        exit: " + status
	if stderr := strings.TrimSpace(a.Stderr); stderr != "" {
		report += "\n        stderr: " + stderr
	}
	return report
}

// Witness runs the same three commands with stderr captured and the exit error
// kept. This is the information Silent discards.
func Witness(interfaceName string, dnsServers []string) ([]Attempt, error) {
	ctlPath, err := exec.LookPath("resolvectl")
	if err != nil {
		return nil, err
	}
	commands := Commands(interfaceName, dnsServers)
	attempts := make([]Attempt, 0, len(commands))
	for _, args := range commands {
		output, err := shell.Exec(ctlPath, args...).Read()
		attempts = append(attempts, Attempt{Args: args, Err: err, Stderr: output})
	}
	return attempts, nil
}

// Status returns `resolvectl status <interface>`, i.e. what the system actually
// ended up with after the writes. An empty interfaceName queries every link.
func Status(interfaceName string) (string, error) {
	ctlPath, err := exec.LookPath("resolvectl")
	if err != nil {
		return "", err
	}
	args := []string{"status"}
	if interfaceName != "" {
		args = append(args, interfaceName)
	}
	return shell.Exec(ctlPath, args...).Read()
}

// Reachable reports whether systemd-resolved answers at all, i.e. whether the
// D-Bus service behind /run/systemd/resolve/io.systemd.Resolve exists. resolvectl
// exits non-zero on every path where it cannot reach resolved - missing socket, or
// an activation attempt that fails because the unit is disabled or masked - so the
// exit status alone is the test, and no message matching is involved (resolvectl
// localises its output).
func Reachable() bool {
	_, err := Status("")
	return err == nil
}

// CheckTrigger guards against a repro that "succeeds" for the wrong reason.
//
// Every resolvectl failure is a non-zero exit, so counting failures proves nothing on
// its own: an unknown interface name fails before resolvectl ever reaches systemd, and
// would happily satisfy a naive "3/3 failed" verdict while the intended trigger —
// resolved absent, or polkit denying — never happened.
//
// Each attempt must have failed *and* its output must contain at least one of the
// given signatures. Resolvectl localises its prose but keeps these identifiers, so
// matching on them is both stable and locale-independent.
func CheckTrigger(attempts []Attempt, signatures ...string) error {
	if len(attempts) == 0 {
		return errors.New("no resolvectl invocation recorded")
	}
	for _, attempt := range attempts {
		if attempt.Err == nil {
			return errors.New("resolvectl " + strings.Join(attempt.Args, " ") + " unexpectedly succeeded")
		}
		matched := false
		for _, signature := range signatures {
			if strings.Contains(attempt.Stderr, signature) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf(
				"resolvectl %s failed for an unrelated reason, so the trigger did not fire: %q",
				strings.Join(attempt.Args, " "),
				strings.TrimSpace(attempt.Stderr),
			)
		}
	}
	return nil
}
