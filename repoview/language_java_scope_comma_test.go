package repoview

import (
	"reflect"
	"strings"
	"testing"
)

func TestJavaLexicalScopePrefixesIgnoreNestedCommas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		source       string
		escapedStart string
	}{
		{
			name: "method parameters",
			source: `class Owner {
    /** method docs */
    void run(
        int first,
        int second
    ) {
        use(first, second);
    }
}`,
			escapedStart: `cl\u0061ss Owner`,
		},
		{
			name: "constructor parameters",
			source: `class Owner {
    /** constructor docs */
    Owner(
        int first,
        int second
    ) {
        use(first, second);
    }
}`,
			escapedStart: `cl\u0061ss Owner`,
		},
		{
			name: "annotation arguments",
			source: `@interface Marker {
    String first();
    String second();
}
class Owner {
    /** constructor docs */
    @Marker(first = "one", second = "two")
    Owner() {}
}`,
			escapedStart: `cl\u0061ss Owner`,
		},
		{
			name: "control condition invocations",
			source: `class Owner {
    void run() {
        if (ready(first, second)) {
            use(first, second);
        }
        for (int index = begin(first, second); index < limit; index++) {
            use(index, second);
        }
    }
}`,
			escapedStart: `cl\u0061ss Owner`,
		},
		{
			name: "enum constructor arguments",
			source: `/** choice docs */
enum Choice {
    FIRST(first, second),
    SECOND(third, fourth)
}`,
			escapedStart: `en\u0075m Choice`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			javaAssertConcreteSyntax(t, test.source)
			lineCount := strings.Count(test.source, "\n") + 1
			concrete := analyzeJavaSource(test.source, lineCount)
			if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
				t.Fatalf("fixture lacks clean concrete authority: tree=%v recovery=%#v",
					concrete.tree != nil, concrete.recoverySpans)
			}

			lexicalSource := test.source
			switch {
			case strings.Contains(lexicalSource, "class Owner"):
				lexicalSource = strings.Replace(lexicalSource, "class Owner", test.escapedStart, 1)
			case strings.Contains(lexicalSource, "enum Choice"):
				lexicalSource = strings.Replace(lexicalSource, "enum Choice", test.escapedStart, 1)
			default:
				t.Fatal("fixture has no escaped declaration target")
			}
			lexical := analyzeJavaSource(lexicalSource, lineCount)
			if !lexical.lexed.translatedEscapes || lexical.tree != nil {
				t.Fatalf("fixture did not force lexical authority: escapes=%v tree=%v",
					lexical.lexed.translatedEscapes, lexical.tree != nil)
			}
			if !reflect.DeepEqual(lexical.scopes, concrete.scopes) {
				t.Fatalf("scope parity mismatch\nconcrete: %#v\nlexical:  %#v",
					concrete.scopes, lexical.scopes)
			}
		})
	}
}

func TestJavaCommaDeclaratorsRetainPerHeaderOpaqueBarrier(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss Owner {
    String first = "allowed", second = "also allowed";
    String retained = "allowed", \u0022hidden\u0022 fake;
    int after;
}`
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if !analysis.lexed.translatedEscapes || analysis.tree != nil {
		t.Fatalf("fixture did not force lexical authority: escapes=%v tree=%v",
			analysis.lexed.translatedEscapes, analysis.tree != nil)
	}
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"Owner", "first", "second", "retained", "after"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}
