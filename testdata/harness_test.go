//go:build linux

// Shared harness for the Bettbox#531 / Bettbox#522 repros in this directory.
//
// It provides three things and nothing else:
//
//   - a `resolvectl` stand-in on PATH, so a reported failure mode can be replayed
//     deterministically without touching the host (systemd-resolved running or not,
//     polkit configured or not, agent registered or not);
//   - an invocation log, so a test can prove the trigger actually fired instead of
//     passing vacuously;
//   - a recording logger, so a test can prove sing-tun emitted nothing.
package tun

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/sing/common/logger"
	"github.com/metacubex/sing/common/shell"
	"github.com/stretchr/testify/require"
)

// reproStubResolvectl installs an executable named `resolvectl` at the front of
// PATH. It appends every invocation to the returned log file, writes stderr to
// stderr and exits 1, i.e. it replays one of the two real failure modes.
func reproStubResolvectl(t *testing.T, stderr string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "invocations.log")
	stderrPath := filepath.Join(dir, "stderr.txt")
	require.NoError(t, os.WriteFile(stderrPath, []byte(stderr), 0o644))
	script := "#!/bin/sh\n" +
		"printf 'resolvectl %s\\n' \"$*\" >> '" + logPath + "'\n" +
		"cat '" + stderrPath + "' >&2\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "resolvectl"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// reproWaitInvocations blocks until resolvectl has been run `count` times and
// returns the recorded command lines.
func reproWaitInvocations(t *testing.T, logPath string, count int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var lines []string
		content, err := os.ReadFile(logPath)
		switch {
		case err == nil:
			if trimmed := strings.TrimRight(string(content), "\n"); trimmed != "" {
				lines = strings.Split(trimmed, "\n")
			}
		case !os.IsNotExist(err):
			t.Fatalf("read invocation log: %v", err)
		}
		if len(lines) >= count {
			return lines
		}
		if time.Now().After(deadline) {
			t.Fatalf("resolvectl was invoked %d time(s), want %d: %q", len(lines), count, lines)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// reproWitness replays one command with stderr captured and the exit error kept,
// which is exactly the information (*NativeTun).setSearchDomainForSystemdResolved
// discards.
func reproWitness(args ...string) (string, error) {
	ctlPath, err := exec.LookPath("resolvectl")
	if err != nil {
		return "", err
	}
	return shell.Exec(ctlPath, args...).Read()
}

// reproOptions is the TUN configuration a Clash.Meta core hands to sing-tun for a
// fake-ip range of 198.18.0.0/15, so the DNS addresses derived from it are the ones
// both issues report: 198.18.0.2 and fdfe:dcba:9876::2.
func reproOptions(interfaceName string, log logger.Logger) Options {
	return Options{
		Name:         interfaceName,
		AutoRoute:    true,
		DNSMode:      DNSModeHijack,
		Inet4Address: []netip.Prefix{netip.MustParsePrefix("198.18.0.1/30")},
		Inet6Address: []netip.Prefix{netip.MustParsePrefix("fdfe:dcba:9876::1/126")},
		Logger:       log,
	}
}

type reproRecord struct {
	Level string
	Args  []any
}

type reproRecordingLogger struct {
	access  sync.Mutex
	records []reproRecord
}

func newReproRecordingLogger() *reproRecordingLogger {
	return &reproRecordingLogger{}
}

func (l *reproRecordingLogger) record(level string, args ...any) {
	l.access.Lock()
	defer l.access.Unlock()
	l.records = append(l.records, reproRecord{Level: level, Args: args})
}

func (l *reproRecordingLogger) Trace(args ...any) { l.record("trace", args...) }
func (l *reproRecordingLogger) Debug(args ...any) { l.record("debug", args...) }
func (l *reproRecordingLogger) Info(args ...any)  { l.record("info", args...) }
func (l *reproRecordingLogger) Warn(args ...any)  { l.record("warn", args...) }
func (l *reproRecordingLogger) Error(args ...any) { l.record("error", args...) }
func (l *reproRecordingLogger) Fatal(args ...any) { l.record("fatal", args...) }
func (l *reproRecordingLogger) Panic(args ...any) { l.record("panic", args...) }

func (l *reproRecordingLogger) Records() []reproRecord {
	l.access.Lock()
	defer l.access.Unlock()
	return append([]reproRecord(nil), l.records...)
}

func (l *reproRecordingLogger) String() string {
	records := l.Records()
	if len(records) == 0 {
		return "<no log record at all>"
	}
	lines := make([]string, 0, len(records))
	for _, record := range records {
		parts := make([]string, 0, len(record.Args))
		for _, arg := range record.Args {
			parts = append(parts, strings.ReplaceAll(fmt.Sprint(arg), "\n", " "))
		}
		lines = append(lines, record.Level+": "+strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
}
