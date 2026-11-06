// Repro: polkit denies the writes because no authentication agent is registered yet.
//
//	systemd-resolved is running and reachable, so the D-Bus call gets as far as the
//	polkit check. The four write actions are declared auth_admin / auth_admin_keep,
//	and a client that starts at login asks before the desktop authentication agent has
//	registered with polkitd. polkit has nowhere to prompt and denies the request:
//	"Access denied as the requested operation requires interactive authentication".
//	sing-tun discards it, and the very same commands succeed a minute later once the
//	agent is up - so nothing records that the first attempt ever failed.
//
// This one needs the environment, not a stub. polkit-denied/Dockerfile builds a
// container where systemd-resolved and polkit are up, the caller is unprivileged, and
// no authentication agent exists:
//
//	podman build -f polkit-denied/Dockerfile -t sing-tun-resolved-repro-polkit-denied .
//	podman run --rm --privileged sing-tun-resolved-repro-polkit-denied
//
// Exit code 0 means the defect reproduced, 3 means the trigger was absent (you are
// root, or an agent is registered).
package main

import (
	"flag"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/local/sing-tun-resolved-repro/internal/resolvectlrepro"
)

func main() {
	interfaceName := flag.String("i", resolvectlrepro.DefaultInterface, "interface to configure")
	flag.Parse()

	fmt.Println("== polkit-denied: polkit denies, no authentication agent registered, sing-tun swallows it ==")
	fmt.Println()

	// Polkit is skipped when the caller is EUID 0, because sd_bus_privilege_op
	// reports the sender as privileged. That is the whole difference between the
	// setuid build (works) and the capabilities build (silently broken).
	if os.Geteuid() == 0 {
		fmt.Println("TRIGGER ABSENT: running as root, so sd_bus_privilege_op() bypasses polkit entirely.")
		fmt.Println("               This trigger needs an unprivileged caller - use the image from")
		fmt.Println("               polkit-denied/Dockerfile, which runs this binary as uid 1000.")
		os.Exit(3)
	}
	fmt.Printf("caller: uid=%d (unprivileged, so polkit is consulted)\n", os.Geteuid())

	if !resolvectlrepro.Reachable() {
		fmt.Println("TRIGGER ABSENT: systemd-resolved does not answer, so this is the resolved-absent trigger.")
		os.Exit(3)
	}
	fmt.Println("precondition ok: systemd-resolved is reachable (unlike the resolved-absent case, the D-Bus call succeeds)")
	fmt.Printf("interface under test: %s\n\n", *interfaceName)

	servers := resolvectlrepro.DNSServers(
		[]netip.Prefix{resolvectlrepro.FakeIPPrefix4},
		[]netip.Prefix{resolvectlrepro.FakeIPPrefix6},
	)
	fmt.Printf("DNS addresses sing-tun would publish: %v\n", strings.Join(servers, " "))
	fmt.Println()

	// (1) What sing-tun does.
	fmt.Println("--- (1) sing-tun's call shape: (*NativeTun).setSearchDomainForSystemdResolved ---")
	if err := resolvectlrepro.Silent(*interfaceName, servers); err != nil {
		fmt.Printf("returned error: %v\n", err)
	} else {
		fmt.Println("returned error: <nil>   <-- configure() calls this a success")
	}
	fmt.Println("log output:    <no log record at all>")

	// (2) What those same three commands actually did.
	fmt.Println()
	fmt.Println("--- (2) the same three commands, stderr captured ---")
	attempts, err := resolvectlrepro.Witness(*interfaceName, servers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run witness: %v\n", err)
		os.Exit(1)
	}
	failed := 0
	for _, attempt := range attempts {
		fmt.Println(attempt)
		if attempt.Err != nil {
			failed++
		}
	}

	// (3) The consequence the user sees: resolved has nothing for this link.
	fmt.Println()
	fmt.Println("--- (3) system state afterwards: `resolvectl status` ---")
	if output, err := resolvectlrepro.Status(*interfaceName); err != nil {
		fmt.Printf("(resolvectl status failed: %v)\n%v\n", err, strings.TrimSpace(output))
	} else {
		fmt.Println(strings.TrimSpace(output))
	}

	fmt.Println()
	if triggerErr := resolvectlrepro.CheckTrigger(attempts, resolvectlrepro.PolkitDenied...); triggerErr != nil {
		fmt.Printf("NOT REPRODUCED: %d/%d writes failed, but not for the reason under test.\n", failed, len(attempts))
		fmt.Printf("  %v\n", triggerErr)
		os.Exit(1)
	}
	if failed != len(attempts) {
		fmt.Printf("NOT REPRODUCED: only %d/%d writes were denied.\n", failed, len(attempts))
		os.Exit(1)
	}
	fmt.Printf("REPRODUCED: %d/%d resolvectl writes denied by polkit,\n", failed, len(attempts))
	fmt.Println("              and sing-tun reported success with no log output. resolved shows no")
	fmt.Println("              DNS scope for the link, so name-based rules and fake-ip silently")
	fmt.Println("              stopped applying.")
}
