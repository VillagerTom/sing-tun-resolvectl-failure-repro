# sing-tun-resolved-repro
#
# Two repros for the `resolvectl` silent error swallowing defect in
# MetaCubeX/sing-tun, plus the patch that fixes it.
#
# `sing-tun` is a git submodule pinned to the revision under test, so `make test`
# runs against exactly the tree the patch targets. The tests have to be in-package
# (they call the unexported setSearchDomainForSystemdResolved), so they are copied
# into the submodule by `make sync` and taken back out by `make clean`; the
# submodule itself is never committed with them.

ENGINE ?= podman
IMAGE_PREFIX ?= sing-tun-resolved-repro

SUBMODULE := sing-tun
TESTS := harness_test.go resolved_unreachable_test.go polkit_denied_test.go
PATCH := patch/0001-log-resolvectl-failures-in-setSearchDomainForSystemdResolved.patch

.PHONY: help submodule sync test clean resolved-unreachable polkit-denied verify-fix all

help:
	@echo 'submodule             fetch/check out the sing-tun revision under test'
	@echo 'sync                  copy testdata/ into $(SUBMODULE)/ (required before test)'
	@echo 'test                  run both in-package repro tests (expected to FAIL - that is the defect)'
	@echo 'verify-fix            apply patch/, re-run test (expected to PASS), then revert'
	@echo 'resolved-unreachable  build + run the systemd-resolved-not-running repro container'
	@echo 'polkit-denied         build + run the polkit-denied repro container (needs --privileged)'
	@echo 'clean                 remove the synced test files'
	@echo 'all                   test + both containers'

submodule:
	git submodule update --init --recursive

sync:
	@for f in $(TESTS); do cp testdata/$$f $(SUBMODULE)/$$f; done
	@echo 'synced: $(TESTS) -> $(SUBMODULE)/'

# Against the unfixed tree this FAILS, on the assertion that the logger should have
# been told the writes failed. A passing run means the patch landed.
# -count=1: never report a cached result - `verify-fix` relies on actually re-running.
test: sync
	cd $(SUBMODULE) && go test -count=1 -run TestRepro -v .

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

resolved-unreachable:
	$(ENGINE) build -f resolved-unreachable/Dockerfile \
		-t $(IMAGE_PREFIX)-resolved-unreachable .
	$(ENGINE) run --rm $(IMAGE_PREFIX)-resolved-unreachable

# --privileged is needed because systemd-resolved drops privileges to the
# systemd-resolve user and refuses to start without CAP_SETUID/CAP_SETPCAP, and
# because the entrypoint creates the tap device resolvectl is pointed at.
polkit-denied:
	$(ENGINE) build -f polkit-denied/Dockerfile \
		-t $(IMAGE_PREFIX)-polkit-denied .
	$(ENGINE) run --rm --privileged $(IMAGE_PREFIX)-polkit-denied

all: test resolved-unreachable polkit-denied
