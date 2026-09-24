# Makefile — сборка nctalk под CGO_ENABLED=0.
#
# CGO_ENABLED=0 обязательно на этой машине (см. CLAUDE.md, dyld: missing LC_UUID).

CGO_ENABLED := 0
export CGO_ENABLED

.PHONY: all build vet test clean help

all: build

help:
	@echo "Цели:"
	@echo "  make build   собрать nctalk (CGO_ENABLED=0)"
	@echo "  make vet     go vet ./... (статика, без запуска)"
	@echo "  make test    go test ./..."
	@echo "  make clean   удалить бинарник"

build:
	@rm -f nctalk
	@echo "→ go build -o nctalk ./cmd/nctalk"
	@go build -o nctalk ./cmd/nctalk

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -f nctalk
