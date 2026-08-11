package navigator

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const javaFindScopeCounterExtension = ".javafindscope"

type javaFindScopeCounters struct {
	prepareScopes     atomic.Int64
	sourceDefinitions atomic.Int64
	navigationScopes  atomic.Int64
	enclosingScopes   atomic.Int64
	cleanSourceLines  atomic.Int64
}

type javaFindScopeCountingLanguage struct {
	javaLanguage
	counters *javaFindScopeCounters
}

var javaFindScopeTestCounters javaFindScopeCounters

func init() {
	registerLanguage(
		languagesByExtension,
		javaFindScopeCountingLanguage{
			javaLanguage: newJavaLanguage(),
			counters:     &javaFindScopeTestCounters,
		},
		javaFindScopeCounterExtension,
	)
}

func (j javaFindScopeCountingLanguage) prepareSource(
	lines []string,
) languageBackend {
	prepared := j.javaLanguage.prepareSource(lines)
	j.javaLanguage = prepared.(javaLanguage)
	return j
}

func (j javaFindScopeCountingLanguage) prepareFindScopeResolver(
	lines []string,
) preparedFindScopeResolver {
	j.counters.prepareScopes.Add(1)
	return j.javaLanguage.prepareFindScopeResolver(lines)
}

func (j javaFindScopeCountingLanguage) sourceDefinitions(
	lines []string,
) []sourceDefinition {
	j.counters.sourceDefinitions.Add(1)
	return j.javaLanguage.sourceDefinitions(lines)
}

func (j javaFindScopeCountingLanguage) navigationScope(
	lines []string,
	lineNo int,
) (int, int) {
	j.counters.navigationScopes.Add(1)
	return j.javaLanguage.navigationScope(lines, lineNo)
}

func (j javaFindScopeCountingLanguage) enclosingScope(
	lines []string,
	lineNo int,
) (int, int) {
	j.counters.enclosingScopes.Add(1)
	return j.javaLanguage.enclosingScope(lines, lineNo)
}

func (j javaFindScopeCountingLanguage) cleanSourceLines(
	lines []string,
	dropComments, dropDocstrings bool,
) []string {
	j.counters.cleanSourceLines.Add(1)
	return j.javaLanguage.cleanSourceLines(lines, dropComments, dropDocstrings)
}

func resetJavaFindScopeTestCounters() {
	javaFindScopeTestCounters.prepareScopes.Store(0)
	javaFindScopeTestCounters.sourceDefinitions.Store(0)
	javaFindScopeTestCounters.navigationScopes.Store(0)
	javaFindScopeTestCounters.enclosingScopes.Store(0)
	javaFindScopeTestCounters.cleanSourceLines.Store(0)
}

func TestJavaFindPrecomputesScopesAndSnippetCleaningOncePerFile(t *testing.T) {
	const methodCount = 256
	source := javaFindScopeScalingSource(methodCount)

	root := t.TempDir()
	writeFile(t, root, "C"+javaFindScopeCounterExtension, source)
	view := mustView(t, root)
	resetJavaFindScopeTestCounters()
	response, err := view.Find("target", Options{
		Include:      IncludeRefs,
		Return:       ReturnScope,
		DropComments: true,
		MaxCodeLines: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != methodCount || response.ResultsTruncated {
		t.Fatalf("results = %d, truncated = %v; want %d, false",
			len(response.Results), response.ResultsTruncated, methodCount)
	}
	for index, result := range response.Results {
		start := 2 + index*4
		wantScope := fmt.Sprintf("method%d", index)
		if result.Scope != wantScope || result.Line != start+1 ||
			result.StartLine != start || result.EndLine != start+3 {
			t.Fatalf("result %d = %#v, want scope %q at %d-%d",
				index, result, wantScope, start, start+3)
		}
		if strings.Contains(result.Code, "occurrence") {
			t.Fatalf("result %d retained dropped comment:\n%s", index, result.Code)
		}
	}
	if got := javaFindScopeTestCounters.prepareScopes.Load(); got != 1 {
		t.Fatalf("prepared scope indexes = %d, want 1", got)
	}
	if got := javaFindScopeTestCounters.sourceDefinitions.Load(); got != 0 {
		t.Fatalf("generic definition fetches = %d, want 0", got)
	}
	if got := javaFindScopeTestCounters.navigationScopes.Load(); got != 0 {
		t.Fatalf("generic navigation scope calls = %d, want 0", got)
	}
	if got := javaFindScopeTestCounters.enclosingScopes.Load(); got != 0 {
		t.Fatalf("generic enclosing scope calls = %d, want 0", got)
	}
	if got := javaFindScopeTestCounters.cleanSourceLines.Load(); got != 1 {
		t.Fatalf("whole-source snippet cleanings = %d, want 1", got)
	}
}

func javaFindScopeScalingSource(methodCount int) string {
	var source strings.Builder
	// A translated keyword keeps every benchmark size on Java's lexical
	// recovery path instead of crossing the concrete parser's token budget.
	source.WriteString("cl\\u0061ss C {\n")
	for index := range methodCount {
		fmt.Fprintf(&source, "    void method%d() {\n", index)
		source.WriteString("        target(); // first occurrence\n")
		source.WriteString("        target(); /* second occurrence */\n")
		source.WriteString("    }\n")
	}
	source.WriteString("}\n")
	return source.String()
}

func TestJavaPreparedFindScopeResolverMatchesExistingSemantics(t *testing.T) {
	const source = `// attached class comment
@Deprecated
class Outer {
    void first() {
        if (ready) {
            target();
        }
        // method comment
    }

    Runnable task = () -> {
        target();
    };

    class Inner {
        void second() {
            target();
        }
    }
}
`
	lines := javaTestLines(source)
	backend := prepareLanguageBackend(newJavaLanguage(), lines).(javaLanguage)
	resolver := backend.prepareFindScopeResolver(lines).(*javaPreparedFindScopeResolver)
	definitions := resolver.sourceDefinitions
	if len(definitions) == 0 ||
		&definitions[0] != &backend.analysis.definitions[0] {
		t.Fatal("Find resolver cloned or replaced immutable Java definitions")
	}
	for lineNo := 1; lineNo <= len(lines); lineNo++ {
		if got, want := resolver.scopeName(lineNo),
			scopeName(lines, lineNo, backend); got != want {
			t.Fatalf("scope name on line %d = %q, want %q", lineNo, got, want)
		}
		gotStart, gotEnd := resolver.navigationScope(lineNo)
		wantStart, wantEnd := backend.navigationScope(lines, lineNo)
		if gotStart != wantStart || gotEnd != wantEnd {
			t.Fatalf("navigation scope on line %d = %d-%d, want %d-%d",
				lineNo, gotStart, gotEnd, wantStart, wantEnd)
		}
		for _, definition := range definitions {
			if got, want := resolver.definitionCount(lineNo, definition.symbol),
				definitionCount(definitions, lineNo, definition.symbol); got != want {
				t.Fatalf("definition count for %q on line %d = %d, want %d",
					definition.symbol, lineNo, got, want)
			}
		}
	}

	allocations := testing.AllocsPerRun(100, func() {
		for lineNo := 1; lineNo <= len(lines); lineNo++ {
			_ = resolver.scopeName(lineNo)
			_, _ = resolver.navigationScope(lineNo)
			_ = resolver.definitionCount(lineNo, "target")
		}
	})
	if allocations != 0 {
		t.Fatalf("prepared scope queries allocate %.2f times per traversal, want 0",
			allocations)
	}
}

func TestJavaPreparedFindScopeResolverIndexesDenseSameLineDefinitions(t *testing.T) {
	const definitionCount = 16 << 10
	definitions := make([]sourceDefinition, 0, definitionCount+1)
	symbols := make([]string, 0, definitionCount)
	for index := range definitionCount {
		symbol := fmt.Sprintf("field%d", index)
		symbols = append(symbols, symbol)
		definitions = append(definitions, sourceDefinition{symbol: symbol, line: 1})
	}
	definitions = append(definitions, sourceDefinition{symbol: symbols[0], line: 1})
	resolver := newJavaPreparedFindScopeResolver(
		[]string{"dense declarations"}, definitions, nil,
	)
	if got := len(resolver.definitionCounts); got != definitionCount {
		t.Fatalf("indexed definition identities = %d, want %d", got, definitionCount)
	}
	if got := resolver.definitionCount(1, symbols[0]); got != 2 {
		t.Fatalf("duplicate definition count = %d, want 2", got)
	}

	lookups := 0
	allocations := testing.AllocsPerRun(10, func() {
		for _, symbol := range symbols {
			lookups += resolver.definitionCount(1, symbol)
		}
	})
	if allocations != 0 {
		t.Fatalf("%d dense definition lookups allocate %.2f times, want 0",
			definitionCount, allocations)
	}
	if lookups == 0 {
		t.Fatal("dense definition lookups were optimized away")
	}
}

func TestJavaPreparedFindScopeResolverDetectsSameSliceMutation(t *testing.T) {
	t.Parallel()

	lines := []string{
		"class Old {",
		"    void before() { target(); }",
		"}",
	}
	backend := prepareLanguageBackend(newJavaLanguage(), lines).(javaLanguage)
	oldResolver := backend.prepareFindScopeResolver(lines).(*javaPreparedFindScopeResolver)
	if got := oldResolver.scopeName(2); got != "before" {
		t.Fatalf("old scope = %q, want before", got)
	}

	lines[0] = "class Fresh {"
	lines[1] = "    void after() { target(); }"
	newResolver := backend.prepareFindScopeResolver(lines).(*javaPreparedFindScopeResolver)
	if got := newResolver.scopeName(2); got != "after" {
		t.Fatalf("mutated scope = %q, want after", got)
	}
	if got := oldResolver.scopeName(2); got != "before" {
		t.Fatalf("old immutable resolver changed to %q", got)
	}
	if got, want := javaDefinitionSymbols(newResolver.sourceDefinitions),
		[]string{"Fresh", "after"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mutated definitions = %#v, want %#v", got, want)
	}
}

func TestJavaPreparedFindScopeResolverSupportsConcurrentReads(t *testing.T) {
	t.Parallel()

	lines := javaTestLines(`class C {
    void first() { target(); }
    void second() { target(); }
}`)
	backend := prepareLanguageBackend(newJavaLanguage(), lines).(javaLanguage)
	resolver := backend.prepareFindScopeResolver(lines).(*javaPreparedFindScopeResolver)
	wantNames := make([]string, len(lines)+1)
	wantScopes := make([]javaLineScope, len(lines)+1)
	for lineNo := 1; lineNo <= len(lines); lineNo++ {
		wantNames[lineNo] = resolver.scopeName(lineNo)
		wantScopes[lineNo].start, wantScopes[lineNo].end = resolver.navigationScope(lineNo)
	}

	const workers = 8
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for range 100 {
				for lineNo := 1; lineNo <= len(lines); lineNo++ {
					if got := resolver.scopeName(lineNo); got != wantNames[lineNo] {
						t.Errorf("scope name on line %d = %q, want %q",
							lineNo, got, wantNames[lineNo])
						return
					}
					start, end := resolver.navigationScope(lineNo)
					if start != wantScopes[lineNo].start || end != wantScopes[lineNo].end {
						t.Errorf("scope on line %d = %d-%d, want %d-%d",
							lineNo, start, end,
							wantScopes[lineNo].start, wantScopes[lineNo].end)
						return
					}
				}
			}
		}()
	}
	wait.Wait()
}

func BenchmarkJavaFindUnlimitedScopeScaling(b *testing.B) {
	for _, methodCount := range []int{1 << 10, 4 << 10, 16 << 10} {
		b.Run(fmt.Sprintf("methods_%d", methodCount), func(b *testing.B) {
			root := b.TempDir()
			source := javaFindScopeScalingSource(methodCount)
			path := filepath.Join(root, "C.java")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				b.Fatal(err)
			}
			view, err := New(root)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(source)))
			b.ResetTimer()
			for range b.N {
				response, findErr := view.Find("target", Options{
					Include:      IncludeRefs,
					Return:       ReturnScope,
					DropComments: true,
					MaxCodeLines: 8,
				})
				if findErr != nil {
					b.Fatal(findErr)
				}
				if len(response.Results) != methodCount || response.ResultsTruncated {
					b.Fatalf("results = %d, truncated = %v; want %d, false",
						len(response.Results), response.ResultsTruncated, methodCount)
				}
			}
		})
	}
}
