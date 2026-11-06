// Repro: systemd-resolved is not running.
//
//	Arch Linux, desktop installed with NetworkManager, TUN enabled, nothing else
//	touched. systemd-resolved is disabled, /etc/resolv.conf is a plain file holding
//	the DHCP-supplied router address. The tunnel comes up, the UI says connected,
//	and the DNS takeover never happened.
//
// Run it in the container built from ./Dockerfile:
//
//	podman build -f resolved-unreachable/Dockerfile -t sing-tun-resolved-repro-resolved-unreachable .
//	podman run --rm sing-tun-resolved-repro-resolved-unreachable
//
// Exit code 0 means the defect reproduced, 3 means the trigger was absent (you are
// running where systemd-resolved works, so there is nothing to swallow).
package main

import (
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/local/sing-tun-resolved-repro/internal/resolvectlrepro"
)

func main() {
	requestedInterface := flag.String("i", "", "interface to configure (default: Bettbox, else the first non-loopback link)")
	flag.Parse()

	fmt.Println("== systemd-resolved is not running: resolvectl fails, sing-tun swallows it ==")
	fmt.Println()

	interfaceName := resolveInterface(*requestedInterface)
	fmt.Printf("interface under test: %s\n", interfaceName)

	// Precondition: resolvectl is present, so LookPath succeeds and sing-tun
	// proceeds; systemd-resolved is not, so nothing is listening.
	if resolvectlrepro.Reachable() {
		fmt.Println("TRIGGER ABSENT: systemd-resolved answers on this machine, so this trigger is not present.")
		fmt.Println("               Use the image from resolved-unreachable/Dockerfile, where it is masked.")
		os.Exit(3)
	}
	fmt.Println("precondition ok: systemd-resolved does NOT answer (the Arch default)")
	if output, err := resolvectlrepro.Status(""); err == nil {
		fmt.Printf("  resolvectl status: %s\n", strings.TrimSpace(firstLine(output)))
	}
	fmt.Println()

	servers := resolvectlrepro.DNSServers(
		[]netip.Prefix{resolvectlrepro.FakeIPPrefix4},
		[]netip.Prefix{resolvectlrepro.FakeIPPrefix6},
	)
	fmt.Printf("DNS addresses sing-tun would publish: %v\n", strings.Join(servers, " "))
	fmt.Println()

	// (1) What sing-tun does. Three writes, all doomed, and the caller is told
	//     nothing at all.
	fmt.Println("--- (1) sing-tun's call shape: (*NativeTun).setSearchDomainForSystemdResolved ---")
	if err := resolvectlrepro.Silent(interfaceName, servers); err != nil {
		fmt.Printf("returned error: %v\n", err)
	} else {
		fmt.Println("returned error: <nil>   <-- configure() calls this a success")
	}
	fmt.Println("log output:    <no log record at all>")
	time.Sleep(500 * time.Millisecond)

	// (2) What those same three commands actually did.
	fmt.Println()
	fmt.Println("--- (2) the same three commands, stderr captured ---")
	attempts, err := resolvectlrepro.Witness(interfaceName, servers)
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

	// (3) The consequence the user sees.
	fmt.Println()
	fmt.Println("--- (3) system state afterwards: `resolvectl status` ---")
	if output, err := resolvectlrepro.Status(interfaceName); err != nil {
		fmt.Printf("(resolvectl status itself fails: %v)\n%v\n", err, strings.TrimSpace(output))
	} else {
		fmt.Println(strings.TrimSpace(output))
	}

	fmt.Println()
	if triggerErr := resolvectlrepro.CheckTrigger(attempts, resolvectlrepro.ResolvedAbsent...); triggerErr != nil {
		fmt.Printf("NOT REPRODUCED: %d/%d writes failed, but not for the reason under test.\n", failed, len(attempts))
		fmt.Printf("  %v\n", triggerErr)
		os.Exit(1)
	}
	if failed != len(attempts) {
		fmt.Printf("NOT REPRODUCED: only %d/%d writes failed.\n", failed, len(attempts))
		os.Exit(1)
	}
	fmt.Printf("REPRODUCED: %d/%d resolvectl writes failed because systemd-resolved is unreachable,\n", failed, len(attempts))
	fmt.Println("              and sing-tun reported success with no log output. The same information")
	fmt.Println("              would appear if the errors were not discarded.")
}

// resolveInterface picks the interface to configure. resolvectl refuses to act on
// an unknown name, so fall back to the first real link when Bettbox does not exist.
func resolveInterface(requested string) string {
	if requested != "" {
		return requested
	}
	if _, err := net.InterfaceByName(resolvectlrepro.DefaultInterface); err == nil {
		return resolvectlrepro.DefaultInterface
	}
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range interfaces {
			if iface.Flags&net.FlagLoopback == 0 {
				fmt.Printf("(Bettbox is not present, using %s instead)\n", iface.Name)
				return iface.Name
			}
		}
	}
	return resolvectlrepro.DefaultInterface
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}
