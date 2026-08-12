.PHONY: generate-grammar generate-csharp-grammar generate-kotlin-grammar generate-swift-grammar

# Set GRAMMAR_LANGUAGE to csharp, kotlin, or swift and GRAMMAR_SOURCE to the
# corresponding checksum-pinned upstream checkout.
export GRAMMAR_LANGUAGE
export GRAMMAR_SOURCE

generate-grammar:
	go run ./internal/cmd/grammar-generator -repo .

generate-csharp-grammar:
	go run ./internal/cmd/grammar-generator -language csharp -repo .

generate-kotlin-grammar:
	go run ./internal/cmd/grammar-generator -language kotlin -repo .

generate-swift-grammar:
	go run ./internal/cmd/grammar-generator -language swift -repo .
