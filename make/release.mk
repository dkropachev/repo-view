.PHONY: release-artifacts release-publish

release-artifacts:
	go run ./internal/cmd/release-artifacts -mode build

release-publish:
	go run ./internal/cmd/release-artifacts -mode publish
