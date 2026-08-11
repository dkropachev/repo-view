package navigator

import (
	"reflect"
	"strings"
	"testing"
)

func TestLanguageRegistrySelectsExplicitBackends(t *testing.T) {
	t.Parallel()
	tests := []struct {
		extension string
		name      string
		backend   any
	}{
		{extension: ".go", name: "go", backend: goLanguage{}},
		{extension: ".py", name: "python", backend: pythonLanguage{}},
		{extension: ".rs", name: "rust", backend: rustLanguage{}},
		{extension: ".js", name: "javascript", backend: javascriptLanguage{}},
		{extension: ".mjs", name: "mjs", backend: javascriptLanguage{}},
		{extension: ".cjs", name: "cjs", backend: javascriptLanguage{}},
		{extension: ".jsx", name: "jsx", backend: javascriptLanguage{}},
		{extension: ".ts", name: "typescript", backend: typescriptLanguage{}},
		{extension: ".tsx", name: "tsx", backend: typescriptLanguage{}},
		{extension: ".mts", name: "mts", backend: typescriptLanguage{}},
		{extension: ".cts", name: "cts", backend: typescriptLanguage{}},
		{extension: ".c", name: "c", backend: cLanguage{}},
		{extension: ".h", name: "c", backend: cLanguage{}},
		{extension: ".cpp", name: "cpp", backend: cppLanguage{}},
		{extension: ".cs", name: "cs", backend: csharpLanguage{}},
		{extension: ".csx", name: "cs", backend: csharpLanguage{}},
		{extension: ".java", name: "java", backend: javaLanguage{}},
		{extension: ".kt", name: "kt", backend: kotlinLanguage{}},
		{extension: ".kts", name: "kt", backend: kotlinLanguage{}},
		{extension: ".swift", name: "swift", backend: swiftLanguage{}},
		{extension: ".mod", name: "mod", backend: modulaLanguage{}},
		{extension: ".def", name: "mod", backend: modulaLanguage{}},
	}

	for _, test := range tests {
		t.Run(test.extension, func(t *testing.T) {
			t.Parallel()
			backend := languageForExtension(test.extension)
			if backend.name() != test.name {
				t.Fatalf("name = %q, want %q", backend.name(), test.name)
			}
			if reflect.TypeOf(backend) != reflect.TypeOf(test.backend) {
				t.Fatalf("backend = %T, want %T", backend, test.backend)
			}
		})
	}
}

func TestSupportedExtensionsComeFromLanguageRegistry(t *testing.T) {
	t.Parallel()
	extensions := SupportedExtensions()
	if len(extensions) != len(languagesByExtension) {
		t.Fatalf("extensions = %d, registry = %d", len(extensions), len(languagesByExtension))
	}
	listed := make(map[string]bool, len(extensions))
	for _, extension := range extensions {
		listed[extension] = true
	}
	for extension := range languagesByExtension {
		if !listed[extension] {
			t.Fatalf("registered extension %q is not searchable", extension)
		}
	}

	first := extensions[0]
	extensions[0] = ".mutated"
	if fresh := SupportedExtensions(); fresh[0] != first {
		t.Fatalf("mutating returned extensions changed registry copy: %#v", fresh)
	}
}

func TestLanguageBackendsOwnDefinitionRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		extension string
		line      string
		want      string
	}{
		{extension: ".go", line: "func (s Service) Run() {}", want: "Run"},
		{extension: ".py", line: "async def fetch():", want: "fetch"},
		{extension: ".rs", line: "pub(crate) async fn fetch() {}", want: "fetch"},
		{extension: ".js", line: "function render() {", want: "render"},
		{extension: ".ts", line: "class Renderer {", want: "Renderer"},
		{extension: ".c", line: "void render(void) {", want: "render"},
		{extension: ".cpp", line: "void render() {", want: "render"},
		{extension: ".java", line: "public void render() {", want: "render"},
		{extension: ".cs", line: "public void Render() {", want: "Render"},
		{extension: ".kt", line: "suspend fun render() {", want: "render"},
		{extension: ".swift", line: "func render() {", want: "render"},
	}

	for _, test := range tests {
		t.Run(test.extension, func(t *testing.T) {
			t.Parallel()
			got, ok := languageForExtension(test.extension).definitionSymbol(test.line)
			if !ok || got != test.want {
				t.Fatalf("definition = %q, %v; want %q, true", got, ok, test.want)
			}
		})
	}
}

func TestLanguageBackendsOwnScopeAndImportRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		extension   string
		source      string
		line        int
		wantStart   int
		wantEnd     int
		importStart int
		importEnd   int
	}{
		{
			name:        "go declarations",
			extension:   ".go",
			source:      "package demo\n\nimport (\n\t\"fmt\"\n)\n\nfunc run() {\n\tif true {\n\t\tfmt.Println()\n\t}\n}\n",
			line:        9,
			wantStart:   7,
			wantEnd:     11,
			importStart: 3,
			importEnd:   5,
		},
		{
			name:        "python indentation",
			extension:   ".py",
			source:      "import os\nfrom pathlib import Path\n\ndef run():\n    if True:\n        print(Path.cwd())\n",
			line:        6,
			wantStart:   5,
			wantEnd:     6,
			importStart: 1,
			importEnd:   2,
		},
		{
			name:        "rust braces",
			extension:   ".rs",
			source:      "use std::io;\n\nfn run() {\n    println!(\"ok\");\n}\n",
			line:        4,
			wantStart:   3,
			wantEnd:     5,
			importStart: 1,
			importEnd:   1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			backend := languageForExtension(test.extension)
			lines := strings.Split(strings.TrimSuffix(test.source, "\n"), "\n")
			start, end := backend.enclosingScope(lines, test.line)
			if start != test.wantStart || end != test.wantEnd {
				t.Fatalf("scope = %d-%d, want %d-%d", start, end, test.wantStart, test.wantEnd)
			}
			importStart, importEnd, ok := backend.importRange(lines)
			if !ok || importStart != test.importStart || importEnd != test.importEnd {
				t.Fatalf(
					"imports = %d-%d, %v; want %d-%d, true",
					importStart,
					importEnd,
					ok,
					test.importStart,
					test.importEnd,
				)
			}
		})
	}
}

func TestLanguageBackendsOwnCommentCleaning(t *testing.T) {
	t.Parallel()
	python := languageForExtension(".py")
	pythonSource := "def run():\n    \"\"\"docs\"\"\"\n    return value  # explanation"
	if got := python.cleanSource(pythonSource, true, true); got != "def run():\n    return value" {
		t.Fatalf("python cleaned source = %q", got)
	}

	goBackend := languageForExtension(".go")
	goSource := "func run() {\n\treturn // explanation\n}"
	if got := goBackend.cleanSource(goSource, true, false); got != "func run() {\n\treturn\n}" {
		t.Fatalf("Go cleaned source = %q", got)
	}
}
