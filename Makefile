GO      ?= go
DIST    := dist
GOFLAGS := -trimpath

.PHONY: all build panel wings test tidy clean run compose-up

all: build

build: panel wings

panel:
	cd panel && $(GO) build $(GOFLAGS) -o ../$(DIST)/kgpanel ./cmd/kgpanel

wings:
	cd wings && $(GO) build $(GOFLAGS) -o ../$(DIST)/wings ./cmd/wings

test:
	cd panel && $(GO) test ./... && cd ../wings && $(GO) test ./...

tidy:
	cd panel && $(GO) mod tidy && cd ../wings && $(GO) mod tidy

run-panel: panel
	$(DIST)/kgpanel --listen :8080 --data ./data --secret changeme

run-wings: wings
	$(DIST)/wings --listen :8081 --token changeme --data ./data

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down
