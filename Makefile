# sing-tun-resolved-repro
#
# Two repros for the `resolvectl` silent error swallowing defect in
# MetaCubeX/sing-tun, plus the PR description that proposes the fix.
#
# `sing-tun` is a git submodule pinned to the revision under test, so `make test`
# runs against exactly the tree the PR targets. The tests have to be in-package
# (they call the unexported setSearchDomainForSystemdResolved), so they are copied
# into the submodule by `make sync` and taken back out by `make clean`; the
# submodule itself is never committed with them.

ENGINE ?= podman
IMAGE_PREFIX ?= sing-tun-resolved-repro

SUBMODULE := sing-tun
TESTS := harness_test.go issue531_test.go issue522_test.go
PATCH := patch/0001-log-resolvectl-failures-in-setSearchDomainForSystemdResolved.patch

.PHONY: help submodule sync test clean issue531 issue522 verify-fix all

help:
	@echo 'submodule  fetch/check out the sing-tun revision under test'
	@echo 'sync       copy testdata/ into $(SUBMODULE)/ (required before test)'
	@echo 'test       run both in-package repro tests (expected to FAIL - that is the defect)'
	@echo 'verify-fix apply patch/, re-run test (expected to PASS), then revert'
	@echo 'issue531   build + run the systemd-resolved-absent repro container'
	@echo 'issue522   build + run the polkit-denied repro container (needs --privileged)'
	@echo 'clean      remove the synced test files'
	@echo 'all        test + verify-fix + issue531 + issue522'

submodule:
	git submodule update --init --recursive

sync:
	@for f in $(TESTS); do cp testdata/$$f $(SUBMODULE)/$$f; done
	@echo 'synced: $(TESTS) -> $(SUBMODULE)/'

# Against the unfixed tree this FAILS, on the assertion that the logger should have
# been told the writes failed. A passing run means the PR landed.
test: sync
	cd $(SUBMODULE) && go test -run TestReproIssue -v .

# The same tests, with the proposed fix applied. Always reverts, pass or fail.
verify-fix:
	cd $(SUBMODULE) && git apply --check ../$(PATCH) || exit 1
	cd $(SUBMODULE) && git apply ../$(PATCH)
	@$(MAKE) --no-print-directory test; status=$$?; \
	cd $(SUBMODULE) && git apply -R ../$(PATCH); \
	echo "reverted patch from $(SUBMODULE)/ (test exit $$status)"; \
	exit $$status

clean:
	@cd $(SUBMODULE) && rm -f $(TESTS)
	@echo 'removed $(TESTS) from $(SUBMODULE)/'

issue531:
	$(ENGINE) build -f issue531/Dockerfile -t $(IMAGE_PREFIX)-531 .
	$(ENGINE) run --rm $(IMAGE_PREFIX)-531

# --privileged is needed because systemd-resolved drops privileges to the
# systemd-resolve user and refuses to start without CAP_SETUID/CAP_SETPCAP.
issue522:
	$(ENGINE) build -f issue522/Dockerfile -t $(IMAGE_PREFIX)-522 .
	$(ENGINE) run --rm --privileged $(IMAGE_PREFIX)-522

all: test issue531 issue522