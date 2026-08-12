SCOPESIFTER_CODEX_BINARY ?= bin/scopesifter-codex
SCOPESIFTER_CODEX_ARGS ?=

.PHONY: scopesifter-codex codex-with-scopesifter
scopesifter-codex:
	mkdir -p "$(dir $(SCOPESIFTER_CODEX_BINARY))"
	go build -trimpath -o "$(SCOPESIFTER_CODEX_BINARY)" ./cmd/scopesifter-codex

codex-with-scopesifter: scopesifter-codex
	"$(SCOPESIFTER_CODEX_BINARY)" $(SCOPESIFTER_CODEX_ARGS)
