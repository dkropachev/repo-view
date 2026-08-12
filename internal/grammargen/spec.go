package grammargen

import "fmt"

const (
	treeSitterGeneratorVersion = "v0.1.0"
	treeSitterCLIVersion       = "0.23.0"
)

type filePin struct {
	path   string
	digest string
	label  string
}

type grammarSpec struct {
	generatedParser   *filePin
	name              string
	upstreamName      string
	packageName       string
	outputDirectory   string
	upstreamCommit    string
	parserPath        string
	dirtyMessage      string
	tableMagic        string
	tableReplacement  string
	rawGoDigest       string
	finalGoDigest     string
	tableDigest       string
	dirtyPaths        []string
	pins              []filePin
	corpusPins        []filePin
	correctABIVersion bool
	splitLexer        bool
}

func specFor(language string) (grammarSpec, error) {
	switch language {
	case "csharp":
		return grammarSpec{
			name:             "csharp",
			upstreamName:     "tree-sitter-c-sharp",
			packageName:      "csharpgrammar",
			outputDirectory:  "internal/csharpgrammar",
			upstreamCommit:   "9150f7d56bb47f1a809fa23623f1ba1413e93fa9",
			parserPath:       "src/parser.c",
			dirtyMessage:     "tree-sitter-c-sharp parser or scanner has local changes",
			tableMagic:       "RVCSHARP",
			tableReplacement: "\tparseTable, smallParseTable := csharpGeneratedParseTables()\n\n",
			dirtyPaths:       []string{"src/parser.c", "src/scanner.c"},
			pins: []filePin{
				{
					path:   "src/parser.c",
					digest: "2549deeed0c8aeb84f42f9ccd3cf9de047a0c609387075a97784fddb2d1770cd",
					label:  "parser.c",
				},
				{
					path:   "src/scanner.c",
					digest: "2ee1241a6a275e72a06838f5df927700bd405c16b48f986e2c33d1264cae4818",
					label:  "scanner.c",
				},
			},
			correctABIVersion: true,
		}, nil
	case "kotlin":
		return grammarSpec{
			name:             "kotlin",
			upstreamName:     "tree-sitter-kotlin",
			packageName:      "kotlingrammar",
			outputDirectory:  "internal/kotlingrammar",
			upstreamCommit:   "1852ea17b7f60fb3f9d84e0b1555d56b46b39fb1",
			parserPath:       "src/parser.c",
			dirtyMessage:     "tree-sitter-kotlin parser or scanner has local changes",
			tableMagic:       "RVKOTLIN",
			tableReplacement: "\tparseTable, smallParseTable := kotlinGeneratedParseTables()\n\n",
			dirtyPaths:       []string{"src/parser.c", "src/scanner.c"},
			pins: []filePin{
				{
					path:   "src/parser.c",
					digest: "70f193db454cfb1315d17d2d85879619e4b62295325bc4cbd4fe0f9fb96098e1",
					label:  "parser.c",
				},
				{
					path:   "src/scanner.c",
					digest: "8a300c7da25290d5de076605fb46cc6b53b188d99aa9e8f34e928dbb7191935f",
					label:  "scanner.c",
				},
			},
		}, nil
	case "swift":
		corpusPins := []filePin{
			{path: "test/corpus/annotations.txt", digest: "dd7a6cf376847fe826f37299751a0845192b8c624aa21c1724ff150de0d32b03", label: "corpus/annotations.txt"},
			{path: "test/corpus/classes.txt", digest: "981c302c6d218d6c3b57087bcd5c5dafe08a6006d2f989a8198d64064c7fe658", label: "corpus/classes.txt"},
			{path: "test/corpus/comments.txt", digest: "7c2083b6d6973dc44e12928a46f0b9356fc16f71da197c40895a0cb6303da80b", label: "corpus/comments.txt"},
			{path: "test/corpus/emojis.txt", digest: "257fba8196000b6d395e4bacd559d6e609c1cd726230c65dcbe0e853725d5e3f", label: "corpus/emojis.txt"},
			{path: "test/corpus/expressions.txt", digest: "d408e6a48bf57a8f171860051db310cd30eea811225258f34a3274e7b862cf1d", label: "corpus/expressions.txt"},
			{path: "test/corpus/functions.txt", digest: "32ff2908246ff5598e0a73cb64e8e6f3384b2889636d689d2d69e36f768be0d8", label: "corpus/functions.txt"},
			{path: "test/corpus/literals.txt", digest: "fb6dfedfd30a487698af0759af15c7b08f0ca2b4ae47efe5228c6180fbe5df2a", label: "corpus/literals.txt"},
			{path: "test/corpus/macros.txt", digest: "625248a1fc204b36e52dd882404f9b8f20aff68ae6c923f94ba679453ae4fb42", label: "corpus/macros.txt"},
			{path: "test/corpus/statements.txt", digest: "073bbe4d3c2edbd57119938eb04d5885013a9032d27825aee5f19fb160ab0553", label: "corpus/statements.txt"},
			{path: "test/corpus/types.txt", digest: "3f8040771bf413c420730688136c863f99514c97cc0372f6b8ff18a6d7006a25", label: "corpus/types.txt"},
		}
		pins := []filePin{
			{path: "src/grammar.json", digest: "081ee7a9601afc12869659d407729a4024e5c8e1c21cc46aed3387502d430156", label: "grammar.json"},
			{path: "src/scanner.c", digest: "380edc27e2020e5ba2d6415c9f6c0065965771d60138ae53372858e7b1f92e3b", label: "scanner.c"},
			{path: "grammar.js", digest: "ca35fad5fa249e836f86127dd3daf0a6d7b647a5dd16a0f45a366fe03a794a6c", label: "grammar.js"},
			{path: "package-lock.json", digest: "1f05b21cc01d5a506ae8ccdf79aba0a5223137b636da03cbb41aa740dbe0c75f", label: "package-lock.json"},
			{path: "LICENSE", digest: "3533cec129bb4bba015c0d61d86dd7c3b7e82110e4d2ff7837a01eff5bad5ccc", label: "LICENSE"},
		}
		pins = append(pins, corpusPins...)
		return grammarSpec{
			name:             "swift",
			upstreamName:     "tree-sitter-swift",
			packageName:      "swiftgrammar",
			outputDirectory:  "internal/swiftgrammar",
			upstreamCommit:   "8d02b7ff390a17a43ce90c4e987c49315cfc4be6",
			parserPath:       "src/parser.c",
			dirtyMessage:     "tree-sitter-swift pinned inputs have local changes",
			tableMagic:       "RVSWIFT!",
			tableReplacement: "\tparseTable, smallParseTable := swiftGeneratedParseTables()\n\n",
			dirtyPaths: []string{
				"src/grammar.json",
				"src/scanner.c",
				"grammar.js",
				"package-lock.json",
				"LICENSE",
				"test/corpus",
			},
			pins:       pins,
			corpusPins: corpusPins,
			generatedParser: &filePin{
				path:   "src/parser.c",
				digest: "9df63e0b6680f0b6cf1f1df613aaff2a7a4a3d9c9eb573b28b5d5c33fdaf7494",
				label:  "generated parser.c",
			},
			rawGoDigest:   "2ac2fa39c03d62e84b4e602366506573c54889ed39054b5476ba7b2ba6ff8e4e",
			finalGoDigest: "4745d5b74c074b322e6bf49330b321ffa9c41b93e851a4cde5a3f1a04d9c1534",
			tableDigest:   "1c17f16cf9d32b9ac851816c940736d04c0ce16b1242883bd4ed758e8245e50b",
			splitLexer:    true,
		}, nil
	default:
		return grammarSpec{}, fmt.Errorf("unsupported grammar language %q (want csharp, kotlin, or swift)", language)
	}
}
