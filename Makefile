# Makefile — сборка nctalk (+ nctalk-call / nctalk-talk) под CGO_ENABLED=0
# со стабильной codesign-подписью.
#
# Зачем подпись: macOS Application Firewall и TCC (микрофон) привязываются к
# code-signature бинарника. Неподписанный/ad-hoc Go-бинарник меняет подпись при
# каждой пересборке → фаервол спрашивает «разрешить входящие» каждый раз заново.
# Стабильная identity (создаётся один раз через `make setup-codesign`) даёт
# постоянный designated requirement → после ОДНОГО разрешения больше не спрашивает.
#
# CGO_ENABLED=0 обязательно на этой машине (см. CLAUDE.md, dyld: missing LC_UUID).

CGO_ENABLED := 0
export CGO_ENABLED

BINS     := nctalk nctalk-call nctalk-talk
IDENTITY ?= nctalk-dev

# pkg-по-бинарнику (чтобы цель работала, даже если cmd/* ещё не всё существует)
PKG.nctalk      := ./cmd/nctalk
PKG.nctalk-call := ./cmd/nctalk-call
PKG.nctalk-talk := ./cmd/nctalk-talk

.PHONY: all build sign build-signed vet test clean setup-codesign help

all: build-signed

help:
	@echo "Цели:"
	@echo "  make build          собрать бинарники (CGO_ENABLED=0, без подписи)"
	@echo "  make build-signed   собрать + подписать stable-identity (по умолчанию)"
	@echo "  make sign           подписать уже собранные бинарники"
	@echo "  make vet            go vet ./... (статика, без запуска)"
	@echo "  make test           go test ./...  (ВНИМАНИЕ: запускает test-binary)"
	@echo "  make setup-codesign ОДИН раз: создать codesign-identity '$(IDENTITY)'"
	@echo "  make clean          удалить бинарники"

build: $(BINS)

# Шаблон: собирает только если pkg-директория существует (nctalk-call/talk могут
# быть ещё не написаны на ранних этапах).
nctalk nctalk-call nctalk-talk:
	@if [ -d "$(PKG.$@)" ]; then \
	  echo "→ go build -o $@ $(PKG.$@)"; \
	  go build -o "$@" "$(PKG.$@)"; \
	else \
	  echo "⚠ пропускаю $@ ($(PKG.$@) ещё не существует)"; \
	fi

sign:
	@rc=0; for b in $(BINS); do \
	  if [ ! -f "$$b" ]; then echo "✗ нет бинарника $$b — собери make build" >&2; rc=1; continue; fi; \
	  codesign -s "$(IDENTITY)" --force --timestamp=none "$$b" \
	    && echo "✔ signed: $$b" || { echo "✗ подпись $$b не удалась (сделай make setup-codesign?)" >&2; rc=1; }; \
	done; exit $$rc

build-signed: build sign

vet:
	go vet ./...

test:
	go test ./...

setup-codesign:
	@bash scripts/setup-codesign.sh

clean:
	rm -f $(BINS)
