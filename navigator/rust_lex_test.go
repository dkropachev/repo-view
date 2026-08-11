package navigator

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRustLexAdversarialInputsRemainBounded(t *testing.T) {
	const caseLimit = 5 * time.Second
	cases := []struct {
		name                  string
		source                string
		wantDefinitions       int
		wantRecovered         int
		singleLineDefinitions bool
	}{
		{
			name:            "wrong delimiter closers",
			source:          strings.Repeat("(", 20_000) + strings.Repeat("]", 20_000),
			wantDefinitions: 0,
		},
		{
			name:            "many opaque macro scopes",
			source:          strings.Repeat("m!{{}}\n", 12_000),
			wantDefinitions: 0,
		},
		{
			name:            "many documented items",
			source:          strings.Repeat("/// docs\nfn repeated() {}\n", 10_000),
			wantDefinitions: 10_000,
		},
		{
			name:                  "many malformed item headers",
			source:                strings.Repeat("fn repeated(\n", 10_000),
			wantDefinitions:       10_000,
			wantRecovered:         9_999,
			singleLineDefinitions: true,
		},
		{
			name:                  "many bodyless function headers",
			source:                strings.Repeat("fn repeated\n", 10_000),
			wantDefinitions:       10_000,
			wantRecovered:         9_999,
			singleLineDefinitions: true,
		},
		{
			name:                  "many malformed impl headers",
			source:                strings.Repeat("impl Repeated (\n", 10_000),
			wantDefinitions:       10_000,
			wantRecovered:         9_999,
			singleLineDefinitions: true,
		},
		{
			name:                  "many unmatched function generics",
			source:                strings.Repeat("fn repeated<\n", 10_000),
			wantDefinitions:       10_000,
			wantRecovered:         9_999,
			singleLineDefinitions: true,
		},
		{
			name:                  "many unmatched impl generics",
			source:                strings.Repeat("impl Repeated <\n", 10_000),
			wantDefinitions:       10_000,
			wantRecovered:         9_999,
			singleLineDefinitions: true,
		},
		{
			name: "many unmatched const comparisons",
			source: strings.Repeat(
				"const Repeated: usize = value <\n",
				10_000,
			),
			wantDefinitions:       10_000,
			wantRecovered:         9_999,
			singleLineDefinitions: true,
		},
		{
			name:                  "many same-line impl candidates",
			source:                "fn broken\n" + strings.Repeat("impl Same ", 20_000),
			wantDefinitions:       2,
			wantRecovered:         2,
			singleLineDefinitions: true,
		},
		{
			name:                  "many same-line const candidates",
			source:                "fn broken< " + strings.Repeat("const Same: usize ", 20_000),
			wantDefinitions:       1,
			singleLineDefinitions: true,
		},
		{
			name: "many prefixed const candidates",
			source: "fn broken<\n" + strings.Repeat(
				"pub const Same: usize <\n",
				10_000,
			),
			wantDefinitions:       10_001,
			wantRecovered:         10_000,
			singleLineDefinitions: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			result := lexRust(test.source)
			if elapsed := time.Since(started); elapsed > caseLimit {
				t.Fatalf("lexRust took %s, limit %s", elapsed, caseLimit)
			}
			if len(test.source) > 0 && len(result.tokens) == 0 {
				t.Fatal("adversarial source unexpectedly produced no tokens")
			}
			if len(result.definitions) != test.wantDefinitions {
				t.Fatalf(
					"definitions = %d, want %d",
					len(result.definitions),
					test.wantDefinitions,
				)
			}
			if len(result.recoveredDefinitions) < test.wantRecovered {
				t.Fatalf(
					"recovered definitions = %d, want at least %d",
					len(result.recoveredDefinitions),
					test.wantRecovered,
				)
			}
			if test.singleLineDefinitions {
				for index := range result.definitions {
					definition := result.definitions[index]
					if definition.scopeStart != definition.line ||
						definition.scopeEnd != definition.line || definition.ownsScope {
						t.Fatalf("definition[%d] crossed recovery boundary: %#v", index, definition)
					}
				}
			}
		})
	}
}

func TestRustMatchDelimitersMatchesReferenceRecovery(t *testing.T) {
	t.Parallel()

	streams := []string{
		"",
		"()[]{}",
		"([)]",
		"{[(])}",
		strings.Repeat("(", 512) + strings.Repeat("]", 512),
	}
	state := uint32(0x5eed1234)
	const alphabet = "()[]{}x"
	for range 2_000 {
		state = state*1_664_525 + 1_013_904_223
		length := int(state % 129)
		var stream strings.Builder
		stream.Grow(length)
		for range length {
			state = state*1_664_525 + 1_013_904_223
			stream.WriteByte(alphabet[int(state)%len(alphabet)])
		}
		streams = append(streams, stream.String())
	}

	for _, stream := range streams {
		tokens := make([]rustToken, 0, len(stream))
		for _, value := range []byte(stream) {
			tokens = append(tokens, rustToken{text: string(value)})
		}
		got := rustMatchDelimiters(tokens)
		want := rustReferenceMatchDelimiters(tokens)
		if !maps.Equal(got, want) {
			t.Fatalf("delimiter recovery for %q = %#v, want %#v", stream, got, want)
		}
	}
}

func rustReferenceMatchDelimiters(tokens []rustToken) map[int]int {
	result := make(map[int]int)
	stack := make([]int, 0, 16)
	for index, token := range tokens {
		switch token.text {
		case "(", "[", "{":
			stack = append(stack, index)
		case ")", "]", "}":
			wanted := map[string]string{")": "(", "]": "[", "}": "{"}[token.text]
			for candidate := len(stack) - 1; candidate >= 0; candidate-- {
				openIndex := stack[candidate]
				if tokens[openIndex].text != wanted {
					continue
				}
				stack = stack[:candidate]
				result[openIndex] = index
				result[index] = openIndex
				break
			}
		}
	}
	return result
}

func TestRustOpaqueTokenRangesAreOrderedAndDisjoint(t *testing.T) {
	t.Parallel()

	const source = `#[cfg(all(unix, any(target_arch = "x86_64", target_arch = "aarch64")))]
macro_rules! outer {
    () => {{ nested!({ fn hidden() {} }); }};
}
wrapper!({ #[allow(dead_code)] fn hidden_too() {} });
pub macro newer($name:ident) { fn also_hidden() {} }
`
	lexer := &rustLexer{source: source, lineStarts: rustLineStarts(source)}
	lexer.scan()
	delimiters := rustMatchDelimiters(lexer.tokens)
	ranges := lexer.opaqueTokenRanges(delimiters)
	if len(ranges) < 4 {
		t.Fatalf("opaque ranges = %#v, want attribute, macro bodies, and invocation", ranges)
	}
	previousEnd := -1
	for index, tokenRange := range ranges {
		if tokenRange.start <= previousEnd || tokenRange.start < 0 ||
			tokenRange.end < tokenRange.start || tokenRange.end >= len(lexer.tokens) {
			t.Fatalf("opaque range[%d] = %#v after end %d", index, tokenRange, previousEnd)
		}
		if tokenRange.visibleOpen >= 0 &&
			(tokenRange.visibleOpen != tokenRange.start ||
				lexer.tokens[tokenRange.visibleOpen].text != "{") {
			t.Fatalf("opaque range[%d] has invalid visible opener: %#v", index, tokenRange)
		}
		previousEnd = tokenRange.end
	}
}

func TestRustLexKeepsMultilineImplTypesInsideFunctionHeaders(t *testing.T) {
	t.Parallel()

	const source = `trait Service {}
fn accepts(
    value: impl Service,
) {}
fn produces() ->
impl Service
{
    loop {}
}
fn produces_reference() ->
&'static mut
impl Service
{
    loop {}
}
fn produces_pointer() ->
*const
impl Service
{
    loop {}
}
fn produces_nested() ->
Option<
impl Service
>
{
    loop {}
}
fn after() {}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{
		"Service",
		"accepts",
		"produces",
		"produces_reference",
		"produces_pointer",
		"produces_nested",
		"after",
	}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	for _, test := range []struct {
		index int
		start int
		end   int
	}{
		{index: 2, start: 5, end: 9},
		{index: 3, start: 10, end: 15},
		{index: 4, start: 16, end: 21},
		{index: 5, start: 22, end: 28},
	} {
		definition := lexed.definitions[test.index]
		if definition.scopeStart != test.start || definition.scopeEnd != test.end ||
			!definition.ownsScope {
			t.Fatalf(
				"multiline impl Trait definition = %#v, want owning scope %d-%d",
				definition,
				test.start,
				test.end,
			)
		}
	}
}

func TestRustLexKeepsBalancedAngleTypeContexts(t *testing.T) {
	t.Parallel()

	const source = `type Callback = Option<
    unsafe extern "system" fn(
        value: usize,
    ) -> bool,
>;
fn generic<
    const N: usize,
>() {}
fn after() {}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"Callback", "generic", "after"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	callback := lexed.definitions[0]
	if callback.scopeStart != 1 || callback.scopeEnd != 5 || callback.ownsScope {
		t.Fatalf("function-pointer alias = %#v, want non-owning scope 1-5", callback)
	}
	generic := lexed.definitions[1]
	if generic.scopeStart != 6 || generic.scopeEnd != 8 || !generic.ownsScope {
		t.Fatalf("const-generic function = %#v, want owning scope 6-8", generic)
	}
}

func TestRustAngleMatchingDoesNotCrossExpressionBlocks(t *testing.T) {
	t.Parallel()

	const source = `fn comparisons(a: i32, b: i32, c: i32, d: i32) {
    if a < b {}
    fn local() {}
    if c > d {}
}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"comparisons", "local"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestRustAngleMatchingDoesNotCrossRecoveredItems(t *testing.T) {
	t.Parallel()

	const source = `fn broken<T
const KEEP: bool = 2 > 1;
fn after() {}
`
	lexed := lexRust(source)
	if len(lexed.definitions) != 3 {
		t.Fatalf("definitions = %#v, want broken, KEEP, and after", lexed.definitions)
	}
	broken, keep, after := lexed.definitions[0], lexed.definitions[1], lexed.definitions[2]
	if broken.symbol != "broken" || broken.scopeEnd != 1 || broken.ownsScope {
		t.Fatalf("broken generic = %#v, want non-owning line 1", broken)
	}
	if keep.symbol != "KEEP" || keep.scopeStart != 2 || keep.scopeEnd != 2 || keep.ownsScope {
		t.Fatalf("following const = %#v, want non-owning line 2", keep)
	}
	if after.symbol != "after" || after.scopeStart != 3 || after.scopeEnd != 3 ||
		!after.ownsScope {
		t.Fatalf("following function = %#v, want owning line 3", after)
	}
}

func TestRustAngleRecoveryDistinguishesNestedConstItemTypes(t *testing.T) {
	t.Parallel()

	const source = `fn broken<T
const KEEP: Option<Result<u8, u16>> = None;
fn after() {}
`
	lexed := lexRust(source)
	if len(lexed.definitions) != 3 {
		t.Fatalf("definitions = %#v, want broken, KEEP, and after", lexed.definitions)
	}
	broken, keep, after := lexed.definitions[0], lexed.definitions[1], lexed.definitions[2]
	if broken.symbol != "broken" || broken.scopeEnd != 1 || broken.ownsScope {
		t.Fatalf("broken generic = %#v, want non-owning line 1", broken)
	}
	if keep.symbol != "KEEP" || keep.scopeStart != 2 || keep.scopeEnd != 2 || keep.ownsScope {
		t.Fatalf("nested-type const = %#v, want non-owning line 2", keep)
	}
	if after.symbol != "after" || after.scopeStart != 3 || after.scopeEnd != 3 ||
		!after.ownsScope {
		t.Fatalf("following function = %#v, want owning line 3", after)
	}
}

func TestRustAngleRecoveryTreatsConstComparisonsAsItems(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{
		"left >= right",
		"left > -1",
		"left > { 1 }",
		"left > (1)",
		"left >> 1",
	} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()
			source := "fn broken<T\nconst KEEP: bool = " + expression + ";\nfn after() {}\n"
			lexed := lexRust(source)
			if len(lexed.definitions) != 3 {
				t.Fatalf("definitions = %#v, want broken, KEEP, and after", lexed.definitions)
			}
			broken, keep, after := lexed.definitions[0], lexed.definitions[1], lexed.definitions[2]
			if broken.symbol != "broken" || broken.scopeEnd != 1 || broken.ownsScope {
				t.Fatalf("broken generic = %#v, want non-owning line 1", broken)
			}
			wantOwnsScope := expression == "left > { 1 }"
			if keep.symbol != "KEEP" || keep.scopeStart != 2 || keep.scopeEnd != 2 ||
				keep.ownsScope != wantOwnsScope {
				t.Fatalf("comparison const = %#v, want ownsScope=%v on line 2", keep, wantOwnsScope)
			}
			if after.symbol != "after" || after.scopeStart != 3 || after.scopeEnd != 3 ||
				!after.ownsScope {
				t.Fatalf("following function = %#v, want owning line 3", after)
			}
		})
	}
}

func TestRustAngleRecoveryTreatsConstComparisonsAfterBrokenImplAsItems(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{"left > right", "left > !right"} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()
			source := "impl<T\nconst KEEP: bool = " + expression + ";\nfn after() {}\n"
			lexed := lexRust(source)
			if len(lexed.definitions) != 2 {
				t.Fatalf("definitions = %#v, want KEEP and after", lexed.definitions)
			}
			keep, after := lexed.definitions[0], lexed.definitions[1]
			if keep.symbol != "KEEP" || keep.scopeStart != 2 || keep.scopeEnd != 2 ||
				keep.ownsScope {
				t.Fatalf("comparison const = %#v, want non-owning line 2", keep)
			}
			if after.symbol != "after" || after.scopeStart != 3 || after.scopeEnd != 3 ||
				!after.ownsScope {
				t.Fatalf("following function = %#v, want owning line 3", after)
			}
		})
	}
}

func TestRustAngleMatchingKeepsConstGenericGroupTails(t *testing.T) {
	t.Parallel()

	const source = `trait Tr {
    fn call<
        #[cfg(any())]
        const N: usize
    >();
}
struct Tuple<
    const N: usize = 1
>([u8; N]);
fn after() {}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"Tr", "call", "Tuple", "after"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	call := lexed.definitions[1]
	if call.scopeStart != 2 || call.scopeEnd != 5 || call.ownsScope {
		t.Fatalf("trait signature = %#v, want non-owning scope 2-5", call)
	}
	tuple := lexed.definitions[2]
	if tuple.scopeStart != 7 || tuple.scopeEnd != 9 || tuple.ownsScope {
		t.Fatalf("tuple struct = %#v, want non-owning scope 7-9", tuple)
	}
}

func TestRustAngleMatchingKeepsBracedTypeMacros(t *testing.T) {
	t.Parallel()

	const source = `macro_rules! ty { () => { u8 } }
fn valid<
    T: Into<ty!{}>,
    const N: usize,
>() {}
fn returns() -> Option<ty!{}> { loop {} }
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"ty", "valid", "returns"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	valid, returns := lexed.definitions[1], lexed.definitions[2]
	if valid.scopeStart != 2 || valid.scopeEnd != 5 || !valid.ownsScope {
		t.Fatalf("generic type macro function = %#v, want owning scope 2-5", valid)
	}
	if returns.scopeStart != 6 || returns.scopeEnd != 6 || !returns.ownsScope {
		t.Fatalf("return type macro function = %#v, want owning line 6", returns)
	}
}

func TestRustAngleMatchingKeepsNegativeImplGenerics(t *testing.T) {
	t.Parallel()

	const source = `#![feature(negative_impls)]
trait Tr {}
struct S<const N: usize>;
impl<
    const N: usize
> !Tr for S<N> {}
fn after() {}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"Tr", "S", "Tr", "after"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	negativeImpl := lexed.definitions[2]
	if negativeImpl.scopeStart != 4 || negativeImpl.scopeEnd != 6 ||
		!negativeImpl.ownsScope {
		t.Fatalf("negative impl = %#v, want owning scope 4-6", negativeImpl)
	}
}

func TestRustAngleMatchingKeepsConstGenericImplTargets(t *testing.T) {
	t.Parallel()

	const source = `struct Foo<const N: usize>;
impl<
    const N: usize
> &Foo<N> {}
impl<
    const N: usize
> *const Foo<N> {}
impl<
    const N: usize
> [u8; N] {}
fn after() {}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"Foo", "Foo", "Foo", "after"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	for _, test := range []struct {
		index int
		start int
		end   int
	}{
		{index: 1, start: 2, end: 4},
		{index: 2, start: 5, end: 7},
	} {
		definition := lexed.definitions[test.index]
		if definition.scopeStart != test.start || definition.scopeEnd != test.end ||
			!definition.ownsScope {
			t.Fatalf("const-generic impl = %#v, want owning scope %d-%d", definition, test.start, test.end)
		}
	}
	after := lexed.definitions[3]
	if after.symbol != "after" || after.scopeStart != 11 || after.scopeEnd != 11 ||
		!after.ownsScope {
		t.Fatalf("following function = %#v, want owning line 11", after)
	}
}

func TestRustAngleRecoveryDoesNotContaminateValidTail(t *testing.T) {
	t.Parallel()

	source := strings.Repeat("const BROKEN: usize = value <\n", 20) + `fn valid<
    const N: usize,
>() {}
`
	lexed := lexRust(source)
	if len(lexed.definitions) != 21 {
		t.Fatalf("definitions = %d, want 20 recovered consts and valid", len(lexed.definitions))
	}
	for index, definition := range lexed.definitions[:20] {
		if definition.symbol != "BROKEN" || definition.line != index+1 ||
			definition.scopeStart != index+1 || definition.scopeEnd > 20 || definition.ownsScope {
			t.Fatalf("malformed definition[%d] = %#v", index, definition)
		}
	}
	valid := lexed.definitions[20]
	if valid.symbol != "valid" || valid.scopeStart != 21 || valid.scopeEnd != 23 ||
		!valid.ownsScope {
		t.Fatalf("valid tail = %#v, want owning scope 21-23", valid)
	}
}

func TestRustLexKeepsPreciseCaptureUseBoundOutOfImports(t *testing.T) {
	t.Parallel()

	const source = `trait Tr {}
impl Tr for u8 {}
fn capture<T: Tr>(value: T) -> impl Tr +
use<T>
{
    value
}
fn after() {}
`
	lexed := lexRust(source)
	if len(lexed.imports) != 0 {
		t.Fatalf("precise capture became import: %#v", lexed.imports)
	}
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"Tr", "Tr", "capture", "after"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	capture := lexed.definitions[2]
	if capture.scopeStart != 3 || capture.scopeEnd != 7 || !capture.ownsScope {
		t.Fatalf("precise capture function = %#v, want owning scope 3-7", capture)
	}
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	backend := newRustLanguage()
	if start, end, ok := backend.importRange(lines); ok {
		t.Fatalf("backend precise capture import = %d-%d, %v", start, end, ok)
	}
	definitions := backend.sourceDefinitions(lines)
	foundCapture := false
	for _, definition := range definitions {
		if definition.symbol != "capture" {
			continue
		}
		foundCapture = true
		if definition.scopeStart != 3 || definition.scopeEnd != 7 || !definition.ownsScope {
			t.Fatalf("backend precise capture function = %#v", definition)
		}
	}
	if !foundCapture {
		t.Fatalf("backend definitions omitted capture: %#v", definitions)
	}
}

func TestRustLexKeepsConstClosureExpressionsInsideStatics(t *testing.T) {
	t.Parallel()

	const source = `#![feature(const_closures)]
static C: fn() =
const move || {};
const D: fn() =
const move || {};
fn after() {}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"C", "D", "after"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	for _, test := range []struct {
		index int
		start int
		end   int
	}{
		{index: 0, start: 2, end: 3},
		{index: 1, start: 4, end: 5},
	} {
		definition := lexed.definitions[test.index]
		if definition.scopeStart != test.start || definition.scopeEnd != test.end ||
			!definition.ownsScope {
			t.Fatalf("const closure = %#v, want owning scope %d-%d", definition, test.start, test.end)
		}
	}
}

func TestRustLexRejectsItemsInNonBlockDelimiterContexts(t *testing.T) {
	t.Parallel()

	const source = `fn outer() {
    let _ = (
        call();
        fn fake() {}
    );
    let _ = ({
        fn legitimate() {}
        legitimate
    });
}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"outer", "legitimate"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestRustLexSplitsClosedConstBlockFromFollowingItem(t *testing.T) {
	t.Parallel()

	const source = `const VALUE: bool = {
    true
}
fn after_block() {}
;
`
	lexed := lexRust(source)
	if len(lexed.definitions) != 2 {
		t.Fatalf("definitions = %#v, want const and following function", lexed.definitions)
	}
	value, after := lexed.definitions[0], lexed.definitions[1]
	if value.symbol != "VALUE" || value.scopeStart != 1 || value.scopeEnd != 3 ||
		!value.ownsScope || !lexed.recoveredDefinitions[rustDefinitionKey(value)] {
		t.Fatalf("closed const block = %#v, want recovered owning scope 1-3", value)
	}
	if after.symbol != "after_block" || after.scopeStart != 4 || after.scopeEnd != 4 ||
		!after.ownsScope {
		t.Fatalf("following function = %#v, want owning scope on line 4", after)
	}
}

func TestRustLexicalElseScopeStartsAfterPreviousBranch(t *testing.T) {
	t.Parallel()

	const source = `if condition {
    first();
} else {
    second();
}
`
	scopes := lexRust(source).scopes
	if !slices.Contains(scopes, rustLineScope{start: 3, end: 5}) {
		t.Fatalf("scopes = %#v, want else scope 3-5", scopes)
	}
	if slices.Contains(scopes, rustLineScope{start: 1, end: 5}) {
		t.Fatalf("else scope crossed the prior branch: %#v", scopes)
	}
}

func TestRustLexMasksExactLiteralsAndNestedComments(t *testing.T) {
	t.Parallel()

	const source = `fn visible<'a>(input: &'a str) {
    'scan: loop {
        let normal = "fn hidden_normal() { /* not a comment */ }";
        let byte = b"fn hidden_byte() {}";
        let c_string = c"fn hidden_c() {}";
        let raw = r###"fn hidden_raw() { \" # }"###;
        let raw_byte = br##"fn hidden_raw_byte() {}"##;
        let raw_c = cr#"fn hidden_raw_c() {}"#;
        let chars = ('{', b'}', '\n', '\u{1F980}', '\'', b'\x7f');
        break 'scan;
    }
}
/* outer comment {
   /* nested comment with fn hidden_comment() {} */
} */
fn after() {}
`
	lexed := lexRust(source)

	wantLiterals := []string{
		`"fn hidden_normal() { /* not a comment */ }"`,
		`b"fn hidden_byte() {}"`,
		`c"fn hidden_c() {}"`,
		`r###"fn hidden_raw() { \" # }"###`,
		`br##"fn hidden_raw_byte() {}"##`,
		`cr#"fn hidden_raw_c() {}"#`,
		`'{'`,
		`b'}'`,
		`'\n'`,
		`'\u{1F980}'`,
		`'\''`,
		`b'\x7f'`,
	}
	gotLiterals := rustLexSpanTexts(source, lexed.stringSpans)
	if !slices.Equal(gotLiterals, wantLiterals) {
		t.Fatalf("literal spans = %#v, want %#v", gotLiterals, wantLiterals)
	}
	if len(lexed.commentSpans) != 1 {
		t.Fatalf("comment spans = %#v, want one nested block-comment span", lexed.commentSpans)
	}
	comment := source[lexed.commentSpans[0].start:lexed.commentSpans[0].end]
	if !strings.HasPrefix(comment, "/* outer comment") ||
		!strings.Contains(comment, "/* nested comment") || !strings.HasSuffix(comment, "} */") {
		t.Fatalf("nested comment span = %q", comment)
	}

	spans := append([]rustByteSpan(nil), lexed.commentSpans...)
	spans = append(spans, lexed.stringSpans...)
	masked := maskRustSource(source, spans)
	for _, retained := range []string{"visible", "after", "'a", "'scan"} {
		if !strings.Contains(masked, retained) {
			t.Errorf("mask consumed %q:\n%s", retained, masked)
		}
	}
	for _, removed := range []string{
		"hidden_normal", "hidden_byte", "hidden_c", "hidden_raw",
		"hidden_raw_byte", "hidden_raw_c", "hidden_comment",
	} {
		if strings.Contains(masked, removed) {
			t.Errorf("mask retained %q:\n%s", removed, masked)
		}
	}
	rustAssertMaskShape(t, source, masked)
}

func TestRustLexPreservesRawNamesAndImplNamingContract(t *testing.T) {
	t.Parallel()

	const source = `fn r#type() {}
struct r#match;
trait r#Trait {}
struct r#Type;
impl<T> crate::r#Trait for crate::r#Type<T> {}
impl<T> crate::r#Type<T> {}
fn r#_() {}
fn r#self() {}
fn r#Self() {}
fn r#super() {}
fn r#crate() {}
fn r#() {}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	want := []string{"r#type", "r#match", "r#Trait", "r#Type", "r#Trait", "r#Type"}
	if !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	for _, symbol := range []string{"r", "r#_", "r#self", "r#Self", "r#super", "r#crate", "r#"} {
		if slices.Contains(got, symbol) {
			t.Errorf("invalid raw identifier became definition %q", symbol)
		}
	}

	rawCount := 0
	for _, token := range lexed.tokens {
		if !token.raw {
			continue
		}
		rawCount++
		if token.text != source[token.start:token.end] || !strings.HasPrefix(token.text, "r#") {
			t.Errorf("raw token lost source spelling: %#v", token)
		}
	}
	if rawCount != 13 {
		t.Fatalf("raw token count = %d, want 13", rawCount)
	}
}

func TestRustLexAcceptsContextualAndEditionSpecificNames(t *testing.T) {
	t.Parallel()

	const source = `fn union() {}
fn raw() {}
fn safe() {}
fn macro_rules() {}
fn async() {}
fn await() {}
fn dyn() {}
fn gen() {}
safe fn safe_item() {}
pub safe extern "C" fn safe_extern() {}
struct union;
impl union {}
trait raw {}
impl raw for union {}
fn type() {}
fn abstract() {}
`
	lexed := lexRust(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	want := []string{
		"union", "raw", "safe", "macro_rules", "async", "await", "dyn", "gen",
		"safe_item", "safe_extern", "union", "union", "raw", "raw",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestRustLexUsesExactRustWhitespaceAndLFLineCommentBoundary(t *testing.T) {
	t.Parallel()

	const comments = "// comment\rfn hidden_after_cr() {}\nfn shown() {}\n"
	commentLexed := lexRust(comments)
	if len(commentLexed.commentSpans) != 1 ||
		comments[commentLexed.commentSpans[0].start:commentLexed.commentSpans[0].end] !=
			"// comment\rfn hidden_after_cr() {}" {
		t.Fatalf("line-comment spans = %#v", commentLexed.commentSpans)
	}
	if len(commentLexed.definitions) != 1 || commentLexed.definitions[0].symbol != "shown" {
		t.Fatalf("line comment exposed fake code: %#v", commentLexed.definitions)
	}

	const exactWhitespace = "fn first() {}\n\vfn visible_vt() {}\n\ffn visible_ff() {}\n\u0085fn visible_nel() {}\n\u200efn visible_lrm() {}\n\u200ffn visible_rlm() {}\n\u2028fn visible_ls() {}\n\u2029fn visible_ps() {}\n\u00a0fn hidden_nbsp() {}\n\u200bfn hidden_zwsp() {}\nfn last() {}\n"
	whitespaceLexed := lexRust(exactWhitespace)
	got := make([]string, 0, len(whitespaceLexed.definitions))
	for _, definition := range whitespaceLexed.definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{
		"first", "visible_vt", "visible_ff", "visible_nel", "visible_lrm",
		"visible_rlm", "visible_ls", "visible_ps", "last",
	}; !slices.Equal(got, want) {
		t.Fatalf("Rust Pattern_White_Space items: got %#v, want %#v", got, want)
	}
}

func TestRustLexExcludesMacroTokenTreesAndAttachesEvidence(t *testing.T) {
	t.Parallel()

	const source = `/// macro docs
macro_rules! declare {
    () => {{
        fn fake_definition() {}
        use fake::MacroImport;
        { fake_nested_scope(); }
    }};
}
wrapper! {
    fn fake_invocation_definition() {}
    use fake::InvocationImport;
    { fake_invocation_scope(); }
}
/// import docs
#[cfg(any(unix, windows))]
pub(crate) use crate::{
    First,
    Second,
};
/// function docs
#[inline]
pub async unsafe extern "C" fn real() {
    if ready() {
        real_scope();
    }
}
`
	lexed := lexRust(source)
	gotDefinitions := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		gotDefinitions = append(gotDefinitions, definition.symbol)
	}
	if want := []string{"declare", "real"}; !slices.Equal(gotDefinitions, want) {
		t.Fatalf("definitions = %#v, want %#v", gotDefinitions, want)
	}
	if len(lexed.imports) != 1 {
		t.Fatalf("imports = %#v, want one real import", lexed.imports)
	}
	wantImport := rustLineSpan{
		start: rustLexLine(t, source, "/// import docs"),
		end:   rustLexLine(t, source, "};\n/// function docs"),
	}
	if lexed.imports[0] != wantImport {
		t.Fatalf("import = %#v, want %#v", lexed.imports[0], wantImport)
	}

	declareLine := rustLexLine(t, source, "/// macro docs")
	realLine := rustLexLine(t, source, "/// function docs")
	if len(lexed.definitions) != 2 ||
		lexed.definitions[0].scopeStart != declareLine ||
		lexed.definitions[1].scopeStart != realLine {
		t.Fatalf("definition evidence was not attached: %#v", lexed.definitions)
	}
	for _, scope := range lexed.scopes {
		for _, hiddenLine := range []int{
			rustLexLine(t, source, "fake_nested_scope"),
			rustLexLine(t, source, "fake_invocation_scope"),
		} {
			if scope.start == hiddenLine && scope.end == hiddenLine {
				t.Errorf("macro token tree emitted scope %#v", scope)
			}
		}
	}
}

func TestRustLexRecoversMalformedItemsCRLFAndInvalidUTF8(t *testing.T) {
	t.Parallel()

	source := "fn first() {}\rfn same_physical_line() {}\r\n" +
		"fn broken(\r\n" +
		"    value: i32,\r\n" +
		"fn later() {}\r\n" +
		"fn " + string([]byte{0xff}) + "() {}\r\n" +
		"fn tail() {}\n"
	lexed := lexRust(source)
	want := []struct {
		symbol string
		line   int
	}{
		{symbol: "first", line: 1},
		{symbol: "same_physical_line", line: 1},
		{symbol: "broken", line: 2},
		{symbol: "later", line: 4},
		{symbol: "tail", line: 6},
	}
	if len(lexed.definitions) != len(want) {
		t.Fatalf("definitions = %#v, want %#v", lexed.definitions, want)
	}
	for index, expected := range want {
		definition := lexed.definitions[index]
		if definition.symbol != expected.symbol || definition.line != expected.line {
			t.Errorf("definition[%d] = %#v, want symbol %q on line %d", index, definition, expected.symbol, expected.line)
		}
	}
	broken := lexed.definitions[2]
	if broken.ownsScope || broken.scopeEnd != 3 {
		t.Fatalf("broken definition did not stop before recovered item: %#v", broken)
	}
}

func TestRustLexRawDelimiterLimitAndUnterminatedRecovery(t *testing.T) {
	t.Parallel()

	hashes255 := strings.Repeat("#", 255)
	valid := "let value = r" + hashes255 + "\"fn hidden() {}\"" + hashes255 + ";\nfn visible() {}\n"
	validLexed := lexRust(valid)
	if len(validLexed.stringSpans) != 1 ||
		valid[validLexed.stringSpans[0].start:validLexed.stringSpans[0].end] !=
			"r"+hashes255+"\"fn hidden() {}\""+hashes255 {
		t.Fatalf("255-hash raw string spans = %#v", validLexed.stringSpans)
	}
	if len(validLexed.definitions) != 1 || validLexed.definitions[0].symbol != "visible" {
		t.Fatalf("definitions after valid raw string = %#v", validLexed.definitions)
	}

	hashes256 := strings.Repeat("#", 256)
	invalid := "let value = r" + hashes256 + "\"fn hidden() {}\"" + hashes256 + ";\nfn visible() {}\n"
	invalidLexed := lexRust(invalid)
	if len(invalidLexed.stringSpans) != 1 ||
		strings.HasPrefix(
			invalid[invalidLexed.stringSpans[0].start:invalidLexed.stringSpans[0].end],
			"r#",
		) {
		t.Fatalf("256 hashes were accepted as a raw delimiter: %#v", invalidLexed.stringSpans)
	}
	if len(invalidLexed.definitions) != 1 || invalidLexed.definitions[0].symbol != "visible" {
		t.Fatalf("definitions after malformed raw string = %#v", invalidLexed.definitions)
	}

	unterminatedChar := "let value = 'not_a_char\nfn recovered() {}\n"
	charLexed := lexRust(unterminatedChar)
	if len(charLexed.stringSpans) != 0 || len(charLexed.definitions) != 1 ||
		charLexed.definitions[0].symbol != "recovered" {
		t.Fatalf("unterminated char recovery = %#v", charLexed)
	}
	unterminatedString := "let value = \"unterminated\nfn hidden() {}\n"
	stringLexed := lexRust(unterminatedString)
	if len(stringLexed.stringSpans) != 1 || len(stringLexed.definitions) != 0 {
		t.Fatalf("unterminated string recovery = %#v", stringLexed)
	}

	adjacent := `fn shown() { let _ = name"fn hidden() {}"; let _ = name'a'; }`
	adjacentLexed := lexRust(adjacent)
	if got := rustLexSpanTexts(adjacent, adjacentLexed.stringSpans); !slices.Equal(got, []string{`"fn hidden() {}"`, `'a'`}) {
		t.Fatalf("adjacent quoted tokens = %#v", got)
	}
	if len(adjacentLexed.definitions) != 1 || adjacentLexed.definitions[0].symbol != "shown" {
		t.Fatalf("adjacent literal exposed fake definition: %#v", adjacentLexed.definitions)
	}
}

func FuzzRustLexNeverPanics(f *testing.F) {
	for _, source := range []string{
		"",
		"fn r#type<'a>(value: &'a str) { 'label: loop { break 'label; } }\n",
		"r###\"raw } /* //\"### /* outer /* inner */ end */\n",
		"macro_rules! m { () => {{ fn hidden() {} }}; }\nfn shown() {}\n",
		"fn broken(\nuse crate::{Item,\nfn later() {}\n",
		string([]byte{'f', 'n', ' ', 0xff, '(', ')', '{', '}', '\r', '\n'}),
	} {
		f.Add(source)
	}

	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 128*1024 {
			t.Skip()
		}
		lexed := lexRust(source)
		lineCount := strings.Count(source, "\n") + 1
		rustAssertLexSpans(t, source, lexed.commentSpans)
		rustAssertLexSpans(t, source, lexed.stringSpans)

		spans := append([]rustByteSpan(nil), lexed.commentSpans...)
		spans = append(spans, lexed.stringSpans...)
		rustAssertMaskShape(t, source, maskRustSource(source, spans))

		previousEnd := 0
		for _, token := range lexed.tokens {
			if token.start < previousEnd || token.start < 0 || token.end <= token.start ||
				token.end > len(source) || token.text != source[token.start:token.end] {
				t.Fatalf("invalid token %#v after byte %d", token, previousEnd)
			}
			previousEnd = token.end
		}
		for _, definition := range lexed.definitions {
			if definition.symbol == "" || definition.line < 1 || definition.line > lineCount ||
				definition.column < 1 || definition.scopeStart < 1 ||
				definition.scopeStart > definition.line || definition.scopeEnd < definition.line ||
				definition.scopeEnd > lineCount {
				t.Fatalf("invalid definition %#v for %d lines", definition, lineCount)
			}
		}
		for _, scope := range lexed.scopes {
			if scope.start < 1 || scope.end < scope.start || scope.end > lineCount {
				t.Fatalf("invalid scope %#v for %d lines", scope, lineCount)
			}
		}
		for _, importSpan := range lexed.imports {
			if importSpan.start < 1 || importSpan.end < importSpan.start ||
				importSpan.end > lineCount {
				t.Fatalf("invalid import %#v for %d lines", importSpan, lineCount)
			}
		}
	})
}

func rustLexSpanTexts(source string, spans []rustByteSpan) []string {
	result := make([]string, 0, len(spans))
	for _, span := range spans {
		result = append(result, source[span.start:span.end])
	}
	return result
}

func rustLexLine(t *testing.T, source, fragment string) int {
	t.Helper()
	offset := strings.Index(source, fragment)
	if offset < 0 {
		t.Fatalf("source does not contain %q", fragment)
	}
	return strings.Count(source[:offset], "\n") + 1
}

func rustAssertLexSpans(t *testing.T, source string, spans []rustByteSpan) {
	t.Helper()
	previousEnd := 0
	for _, span := range spans {
		if span.start < previousEnd || span.start < 0 || span.end <= span.start ||
			span.end > len(source) {
			t.Fatalf("invalid span %#v after byte %d for %d-byte source", span, previousEnd, len(source))
		}
		previousEnd = span.end
	}
}

func rustAssertMaskShape(t *testing.T, source, masked string) {
	t.Helper()
	if len(masked) != len(source) {
		t.Fatalf("masked length = %d, want %d", len(masked), len(source))
	}
	for index := range len(source) {
		if (source[index] == '\r' || source[index] == '\n') && masked[index] != source[index] {
			t.Fatalf("mask changed line ending at byte %d", index)
		}
	}
}
