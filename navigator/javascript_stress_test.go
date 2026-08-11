package navigator

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestJavaScriptPreparedBackendSupportsConcurrentSameAnalysis(t *testing.T) {
	lines := make([]string, 0, 2_048)
	for index := range 1_024 {
		lines = append(lines,
			fmt.Sprintf("/* comment %04d */", index),
			fmt.Sprintf(`const value%d = "hidden-%04d";`, index, index),
		)
	}
	prepared, ok := prepareLanguageBackend(
		newJavaScriptLanguage("javascript"), lines,
	).(javascriptLanguage)
	if !ok || prepared.analysis == nil || len(prepared.analysis.commentSpans) < 2 {
		t.Fatal("JavaScript backend was not prepared with comment spans")
	}

	// Stored spans are logically unordered input to the masking helper. Keeping
	// them reversed makes an accidental in-place normalization reliably visible
	// to the race detector when every goroutine begins masking at once.
	slices.Reverse(prepared.analysis.commentSpans)
	start := make(chan struct{})
	results := make(chan error, 32)
	for range cap(results) {
		go func() {
			<-start
			cleaned := prepared.cleanSourceLines(lines, true, false)
			ignored := prepared.ignoredSearchLines(lines, true, false)
			searchable := prepared.searchLines(lines, true, true)
			if len(cleaned) != len(lines) || len(searchable) != len(lines) {
				results <- fmt.Errorf(
					"derived line counts = cleaned %d, searchable %d; want %d",
					len(cleaned), len(searchable), len(lines),
				)
				return
			}
			for line := 0; line < len(lines); line += 2 {
				if cleaned[line] != "" || !ignored[line+1] ||
					strings.Contains(searchable[line], "comment") ||
					strings.Contains(searchable[line+1], "hidden") {
					results <- fmt.Errorf(
						"inconsistent derived views at lines %d-%d: cleaned=%q ignored=%v searchable=%q/%q",
						line+1, line+2, cleaned[line], ignored[line+1],
						searchable[line], searchable[line+1],
					)
					return
				}
			}
			results <- nil
		}()
	}
	close(start)
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestJavaScriptLexicalFallbackHandlesLargeAdversarialInputs(t *testing.T) {
	t.Run("deeply_nested_controls", func(t *testing.T) {
		const depth = 20_000
		source := strings.Repeat("if (ready) ", depth) + `require("dependency");`
		lexed := javascriptLexicalOnlyForTest(source)
		if want := []javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(lexed.imports, want) {
			t.Fatalf("nested-control imports = %#v, want %#v", lexed.imports, want)
		}
	})

	t.Run("repeated_malformed_else", func(t *testing.T) {
		const count = 20_000
		source := strings.Repeat("else value ", count) + "\nfunction after() {}"
		lexed := javascriptLexicalOnlyForTest(source)
		if !javascriptStressHasDefinition(lexed, "after") {
			t.Fatal("malformed else chain lost the trailing definition")
		}
	})

	t.Run("long_non_commonjs_assignment_chain", func(t *testing.T) {
		const count = 20_000
		source := strings.Repeat("value = ", count) + "0;\nfunction after() {}"
		lexed := javascriptLexicalOnlyForTest(source)
		if !javascriptStressHasDefinition(lexed, "after") {
			t.Fatal("assignment chain lost the trailing definition")
		}
	})

	t.Run("many_requires_in_one_statement", func(t *testing.T) {
		const count = 12_000
		source := strings.Repeat(`require("dependency"),`, count) + "0;"
		lexed := javascriptLexicalOnlyForTest(source)
		if want := []javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(lexed.imports, want) {
			t.Fatalf("repeated-require imports = %#v, want %#v", lexed.imports, want)
		}
	})

	t.Run("many_strong_callable_definitions", func(t *testing.T) {
		const count = 10_000
		var source strings.Builder
		source.Grow(count * 20)
		source.WriteString("const ")
		for index := range count {
			if index > 0 {
				source.WriteByte(',')
			}
			source.WriteByte('f')
			source.WriteString(strconv.Itoa(index))
			source.WriteString(" = () => 0")
		}
		source.WriteByte(';')

		lexed := javascriptLexicalOnlyForTest(source.String())
		if len(lexed.definitions) != count {
			t.Fatalf("callable definition count = %d, want %d", len(lexed.definitions), count)
		}
		if first, last := lexed.definitions[0], lexed.definitions[len(lexed.definitions)-1]; first.definition.symbol != "f0" || last.definition.symbol != "f9999" ||
			!first.strong || !last.strong {
			t.Fatalf("callable definition bounds = %#v, %#v", first, last)
		}
	})

	t.Run("malformed_multiline_initializers", func(t *testing.T) {
		const count = 20_000
		var source strings.Builder
		for index := range count {
			fmt.Fprintf(&source, "const value%d =\n", index)
		}
		source.WriteString("0;\nfunction after() {}")
		lexed := javascriptLexicalOnlyForTest(source.String())
		if !javascriptStressHasDefinition(lexed, "after") {
			t.Fatal("malformed initializer chain lost the trailing definition")
		}
	})

	t.Run("commonjs_assignment_chain", func(t *testing.T) {
		const count = 20_000
		var source strings.Builder
		for index := range count {
			fmt.Fprintf(&source, "exports.value%d = ", index)
		}
		source.WriteString("() => 0;")
		lexed := javascriptLexicalOnlyForTest(source.String())
		if len(lexed.definitions) != 1 ||
			lexed.definitions[0].definition.symbol != "value19999" {
			t.Fatalf("CommonJS chain definitions = %#v, want final assignment only", lexed.definitions)
		}
	})

	t.Run("many_requires_in_one_jsx_owner", func(t *testing.T) {
		const wrapperDepth = 4_000
		const requireCount = 12_000
		var source strings.Builder
		source.Grow(wrapperDepth*2 + requireCount*24)
		source.WriteString("const view = ")
		source.WriteString(strings.Repeat("(", wrapperDepth))
		source.WriteString("<Panel>")
		for range requireCount {
			source.WriteString(`{require("dependency")}`)
		}
		source.WriteString("</Panel>")
		source.WriteString(strings.Repeat(")", wrapperDepth))
		source.WriteByte(';')

		lexed := javascriptLexicalOnlyForTest(source.String())
		if want := []javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(lexed.imports, want) {
			t.Fatalf("JSX-owner imports = %#v, want %#v", lexed.imports, want)
		}
	})

	t.Run("many_jsx_roots_in_one_statement", func(t *testing.T) {
		const count = 16_000
		var source strings.Builder
		source.Grow(count * 40)
		source.WriteString("const values = [")
		for index := range count {
			if index > 0 {
				source.WriteByte(',')
			}
			source.WriteString(`<A>{require("dependency")}</A>`)
		}
		source.WriteString("];\n")

		lexed := javascriptLexicalOnlyForTest(source.String())
		if want := []javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(lexed.imports, want) {
			t.Fatalf("multi-root JSX imports = %#v, want %#v", lexed.imports, want)
		}
	})

	t.Run("await_at_deep_cached_function_context", func(t *testing.T) {
		const depth = 20_000
		var source strings.Builder
		source.Grow(depth*20 + 64)
		source.WriteString("async function outer() {")
		source.WriteString(strings.Repeat("{", depth))
		for range depth {
			source.WriteString("await /target/;")
		}
		source.WriteString(strings.Repeat("}", depth+1))

		_, literals := javascriptFallbackMasks(source.String())
		if len(literals) != depth {
			t.Fatalf("deep await regex count = %d, want %d", len(literals), depth)
		}
	})

	t.Run("await_after_many_unclosed_callable_prefixes", func(t *testing.T) {
		const depth = 20_000
		source := strings.Repeat("function pending(", depth) +
			strings.Repeat("await / divisor /;", depth)
		_, literals := javascriptFallbackMasks(source)
		if len(literals) != 0 {
			t.Fatalf("malformed pending-callable literals = %d, want none", len(literals))
		}
	})

	t.Run("closing_many_unfinished_callable_prefixes", func(t *testing.T) {
		const depth = 64_000
		source := strings.Repeat("function pending(", depth) + strings.Repeat(")", depth)
		_, literals := javascriptFallbackMasks(source)
		if len(literals) != 0 {
			t.Fatalf("closed malformed-callable literals = %d, want none", len(literals))
		}
	})
}

func javascriptStressHasDefinition(lexed javascriptLexResult, symbol string) bool {
	for _, candidate := range lexed.definitions {
		if candidate.definition.symbol == symbol {
			return true
		}
	}
	return false
}
