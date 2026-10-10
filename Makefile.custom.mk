##@ Security

# govulncheck reports the vulnerabilities whose symbols this module's code
# reaches, so a finding here is one every CLI that embeds the library inherits
# (nancy, which the generated CI runs, lists CVEs by module and misses an
# advisory such as GO-2026-5932 that has no CVE). The scanner runs from its own
# module at the current release and reads the live database at vuln.go.dev.
.PHONY: govulncheck
govulncheck: ## Runs govulncheck against the packages this module compiles.
	@echo "====> $@"
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# `make test` is what CI runs (the go-build job's test_target), so the scan
# chained into it keeps the library clean for its consumers.
test: govulncheck
