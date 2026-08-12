.PHONY: scopesifter-codex
scopesifter-codex:
	mkdir -p bin
	go build -trimpath -o bin/scopesifter-codex ./cmd/scopesifter-codex
