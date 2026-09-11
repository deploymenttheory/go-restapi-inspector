.PHONY: build test vet fmt-check verify lint vuln report-browser

build:
	go build -o bin/restapi-inspector ./cmd/restapi-inspector

test:
	go test -race ./...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l cmd internal examples)" || (gofmt -l cmd internal examples; exit 1)

verify: fmt-check test vet build

lint:
	golangci-lint run --config .golangci.yml

vuln:
	govulncheck ./...

report-browser:
	REPORT_BROWSER_DIR=$(CURDIR)/tmp/report-browser go test ./internal/report -run '^TestBrowserFixtures$$' -count=1
	cd tests/report-browser && npm test
