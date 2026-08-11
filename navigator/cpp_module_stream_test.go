package navigator

import (
	"slices"
	"strings"
	"testing"
)

func TestCPPModuleStreamPreservesBraceDepthAcrossRetainedGap(t *testing.T) {
	t.Parallel()

	filler := strings.Repeat(";\n", cppMaximumRetainedTokens+2048)
	source := "int outer() {\n" + filler + `module nested.phantom;
import nested.dep;
}
export module tail.core;
import tail.dep;
`
	lexed := lexCPP(source)
	if !lexed.truncated {
		t.Fatal("fixture did not cross the retained-token frontier")
	}
	if got := cppDefinitionSymbols(lexed.trustedDefinitions); !slices.Equal(got, []string{"tail.core"}) {
		t.Fatalf("trusted module definitions = %#v, want tail.core only", got)
	}
	if len(lexed.imports) != 1 {
		t.Fatalf("imports = %#v, want top-level tail.dep only", lexed.imports)
	}
	spanText := cppModuleStreamSpanText(source, lexed.moduleSpans)
	for _, want := range []string{
		"module nested.phantom;",
		"import nested.dep;",
		"export module tail.core;",
		"import tail.dep;",
	} {
		if !slices.Contains(spanText, want) {
			t.Errorf("contextual spans = %#v, missing %q", spanText, want)
		}
	}

	definitions := cppDefinitionSymbols(newCPPLanguage().sourceDefinitions(cppTestLines(source)))
	for _, phantom := range []string{"nested.phantom", "nested", "phantom", "nested.dep", "dep"} {
		if slices.Contains(definitions, phantom) {
			t.Errorf("nested module/import operand %q became a definition: %#v", phantom, definitions)
		}
	}
	for _, want := range []string{"outer", "tail.core"} {
		if !slices.Contains(definitions, want) {
			t.Errorf("definitions = %#v, missing %q", definitions, want)
		}
	}
}

func TestCPPModuleStreamRecoversDeclarationsInsideRetainedGap(t *testing.T) {
	t.Parallel()

	half := cppMaximumRetainedTokens/2 + 2048
	source := strings.Repeat(";", half) +
		"module broken::phantom; export module middle.core:part; import middle.dep;" +
		strings.Repeat(";", half)
	lexed := lexCPP(source)
	if !lexed.truncated {
		t.Fatal("fixture did not cross the retained-token frontier")
	}
	if got := cppDefinitionSymbols(lexed.trustedDefinitions); !slices.Equal(got, []string{"middle.core:part"}) {
		t.Fatalf("middle module definitions = %#v", got)
	}
	if len(lexed.imports) != 1 || lexed.imports[0] != (cLineSpan{start: 1, end: 1}) {
		t.Fatalf("middle imports = %#v, want line 1", lexed.imports)
	}
	spanText := cppModuleStreamSpanText(source, lexed.moduleSpans)
	if !slices.Equal(spanText, []string{
		"module broken::phantom;",
		"export module middle.core:part;",
		"import middle.dep;",
	}) {
		t.Fatalf("middle module spans = %#v", spanText)
	}
	fallbackDefinitions := cppDefinitionSymbols(lexed.fallbackDefinitions)
	for _, phantom := range []string{"broken", "phantom", "middle", "core", "part", "dep"} {
		if slices.Contains(fallbackDefinitions, phantom) {
			t.Errorf("streamed module operand %q became a fallback definition: %#v",
				phantom, fallbackDefinitions)
		}
	}
}

func TestCPPModuleStreamRecordsMalformedAndCanonicalTriviaSpans(t *testing.T) {
	t.Parallel()

	const source = `module foo::bar;
module ghost();
import <>;
module;
module : /* fragment trivia */ private;
export /* declaration trivia */ module de\
mo /* component trivia */ . core : part;
export\
 import demo /* import trivia */ . dep;
import/**/"adjacent.hpp";
int module();
int import();
`
	lexed := lexCPP(source)
	if got := cppDefinitionSymbols(lexed.trustedDefinitions); !slices.Equal(got, []string{"demo.core:part"}) {
		t.Fatalf("canonical module definitions = %#v", got)
	}
	if len(lexed.imports) != 2 {
		t.Fatalf("canonical imports = %#v, want named and adjacent header imports", lexed.imports)
	}

	spanText := cppModuleStreamSpanText(source, lexed.moduleSpans)
	for _, want := range []string{
		"module foo::bar;",
		"module ghost();",
		"import <>;",
		"module;",
		"module : /* fragment trivia */ private;",
		"export /* declaration trivia */ module de\\\nmo /* component trivia */ . core : part;",
		"export\\\n import demo /* import trivia */ . dep;",
		"import/**/\"adjacent.hpp\";",
	} {
		if !slices.Contains(spanText, want) {
			t.Errorf("contextual spans = %#v, missing %q", spanText, want)
		}
	}
	for _, unexpected := range []string{"int module();", "int import();"} {
		if slices.Contains(spanText, unexpected) {
			t.Errorf("contextual function %q became a module span", unexpected)
		}
	}

	definitions := cppDefinitionSymbols(newCPPLanguage().sourceDefinitions(cppTestLines(source)))
	for _, want := range []string{"demo.core:part", "module", "import"} {
		if !slices.Contains(definitions, want) {
			t.Errorf("contextual function definitions = %#v, missing %q", definitions, want)
		}
	}
	for _, phantom := range []string{"foo", "bar", "ghost"} {
		if slices.Contains(definitions, phantom) {
			t.Errorf("malformed module operand %q became a definition: %#v", phantom, definitions)
		}
	}
}

func TestCPPModuleStreamRejectsUnsplicedNewlinesInHeaderImports(t *testing.T) {
	t.Parallel()

	invalid := []struct {
		name   string
		source string
	}{
		{name: "before header", source: "import\n<one/two>;\n"},
		{name: "line feed", source: "import <one/\ntwo>;\n"},
		{name: "carriage return", source: "import <one/\rtwo>;\n"},
		{name: "carriage return line feed", source: "import <one/\r\ntwo>;\n"},
		{name: "multiline block comment", source: "import <one/* gap\n*/two>;\n"},
		{name: "line comment", source: "import <one// gap\ntwo>;\n"},
		{name: "before terminator", source: "import <one/two>\n;\n"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if imports := lexCPP(test.source).imports; len(imports) != 0 {
				t.Errorf("imports = %#v, want no header import across an unspliced newline", imports)
			}
		})
	}

	valid := []struct {
		name   string
		source string
	}{
		{name: "single line", source: "import <one/two>;\n"},
		{name: "splice before header", source: "import\\\n <one/two>;\n"},
		{name: "spliced line feed", source: "import <one/\\\ntwo>;\n"},
		{name: "spliced carriage return line feed", source: "import <one/\\\r\ntwo>;\n"},
		{name: "splice before terminator", source: "import <one/two>\\\n;\n"},
		{name: "splice after export", source: "export\\\n import <one/two>;\n"},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if imports := lexCPP(test.source).imports; len(imports) != 1 {
				t.Errorf("imports = %#v, want one valid header import", imports)
			}
		})
	}

	t.Run("unspliced export boundary recovers next import", func(t *testing.T) {
		t.Parallel()
		imports := lexCPP("export\nimport <one/two>;\n").imports
		if len(imports) != 1 || imports[0] != (cLineSpan{start: 2, end: 2}) {
			t.Errorf("imports = %#v, want standalone header import on line 2", imports)
		}
	})
}

func TestCPPModuleStreamAcceptsTrailingAttributeSpecifiers(t *testing.T) {
	t.Parallel()

	const source = `export module decorated.core [[vendor::module_tag]];
import decorated.dep [[vendor::named_import]];
import <decorated/header.hpp> [[vendor::angle_import]];
import "decorated/header.hpp" [[vendor::quoted_import]];
`
	lexed := lexCPP(source)
	if got := cppDefinitionSymbols(lexed.trustedDefinitions); !slices.Equal(
		got, []string{"decorated.core"},
	) {
		t.Fatalf("attributed module definitions = %#v, want decorated.core", got)
	}
	if len(lexed.imports) != 3 {
		t.Fatalf("attributed imports = %#v, want named, angle, and quoted imports",
			lexed.imports)
	}
}

func TestCPPModuleStreamRejectsContextualKeywordsInsideModuleNames(t *testing.T) {
	t.Parallel()

	const source = `export module module;
module demo.import;
module demo:module;
import import;
import demo.module;
import :import;
int module();
int import();
`
	lexed := lexCPP(source)
	if got := cppDefinitionSymbols(lexed.trustedDefinitions); len(got) != 0 {
		t.Fatalf("contextual-keyword module definitions = %#v, want none", got)
	}
	if len(lexed.imports) != 0 {
		t.Fatalf("contextual-keyword module imports = %#v, want none", lexed.imports)
	}
	definitions := cppDefinitionSymbols(
		newCPPLanguage().sourceDefinitions(cppTestLines(source)),
	)
	for _, want := range []string{"module", "import"} {
		if !slices.Contains(definitions, want) {
			t.Errorf("ordinary contextual function %q missing from %#v", want, definitions)
		}
	}
}

func TestCPPModuleStreamBoundsAttributeNestingAndRecovers(t *testing.T) {
	t.Parallel()

	atLimit := "export module at.limit [[vendor::tag(" +
		strings.Repeat("(", cppMaximumModuleAttributeDepth-1) + "value" +
		strings.Repeat(")", cppMaximumModuleAttributeDepth-1) + ")]];"
	overLimit := "export module too.deep [[vendor::tag(" +
		strings.Repeat("(", cppMaximumModuleAttributeDepth) + "value" +
		strings.Repeat(")", cppMaximumModuleAttributeDepth) + ")]];"
	source := atLimit + " " + overLimit + " export module after.depth;"
	lexed := lexCPP(source)
	if got, want := cppDefinitionSymbols(lexed.trustedDefinitions),
		[]string{"at.limit", "after.depth"}; !slices.Equal(got, want) {
		t.Fatalf("bounded attributed module definitions = %#v, want %#v", got, want)
	}
	if len(lexed.moduleSpans) != 3 {
		t.Fatalf("module spans after attribute overflow = %#v, want three", lexed.moduleSpans)
	}
}

func TestCPPModuleStreamOperandBudgetResynchronizesAtSemicolon(t *testing.T) {
	t.Parallel()

	source := "module " + strings.Repeat("part.", cppMaximumModuleTokens+32) +
		"last; export module after.budget;"
	lexed := lexCPP(source)
	if got := cppDefinitionSymbols(lexed.trustedDefinitions); !slices.Equal(got, []string{"after.budget"}) {
		t.Fatalf("post-budget module definitions = %#v", got)
	}
	if len(lexed.moduleSpans) != 2 {
		t.Fatalf("module spans = %#v, want oversized and recovered statements", lexed.moduleSpans)
	}
}

func cppModuleStreamSpanText(source string, spans []cByteSpan) []string {
	result := make([]string, 0, len(spans))
	for _, span := range spans {
		if span.start >= 0 && span.end >= span.start && span.end <= len(source) {
			result = append(result, source[span.start:span.end])
		}
	}
	return result
}
