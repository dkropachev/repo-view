package projectcheck

// approvedDynamicProcessFileSHA256 binds complete reviewed source files that
// must invoke an authenticated or runtime-selected native executable. Any byte
// change invalidates the exception and requires another review; new files fail
// closed. Explicit script-runtime names remain forbidden even in these files.
var approvedDynamicProcessFileSHA256 = map[string]string{
	"cmd/scopesifter-codex/main_test.go":              "590f9139652005d468a4a39c80f7a38d6d8ffee51d40bd4a81382e95df1860d6",
	"cmd/scopesifter-codex/signal_unix_test.go":       "ea182d1cb5ee4c0cee9edd0d28fdd622a6b1d45d5593e3eb06e4872f7cd4679e",
	"cmd/scopesifter-validate/main.go":                "d028f17c3928540d1cde36ed40242e0cfafc026c457dc92c22e49e601dcc67f4",
	"cmd/scopesifter-validate/main_test.go":           "5e90fdae3873bdff07019ca15afcece3bf64659805f40a916201586ec020b3ce",
	"cmd/scopesifter/main_test.go":                    "609adb0286f3bba008921969fc82f4cc284eb502e88c81b9b1b834d18ed46ebb",
	"cmd/scopesifter/mcp_test.go":                     "49a30cb5257d6329134f319942d096a1853fcaa29372a439a218dbb46956d9ea",
	"internal/codexlauncher/exit_status_unix_test.go": "25dbe0e1bd3f9ba45d5e4c14cc7fbce880b8b77bc11d76ccc0a2d5fa4977cfe6",
	"internal/codexlauncher/launcher.go":              "b8801189956a54769404dcf548cb16814993626c3f5d0446464345b1e747f0d0",
	"internal/grammargen/runner.go":                   "3a63d6b844d541c1a6064de0bb369a341687e8e1e53e75eb4636085b4ca38e34",
	"internal/grammargen/runner_test.go":              "4163aaae35594de3ecd13ee4c9379ac283d4da722e7ca00ab98ee1d6ad2b58b6",
	"internal/processpolicy/native.go":                "a250c2408038b936ee33f4325d301f6ee6146d217a446fc69c36d874223f1a31",
	"internal/projectcheck/check.go":                  "58e76c634f30cbfba54868bf2b07bc99a8717a0c44b98e82641dd9ec7e1eb9ab",
	"internal/projectcheck/check_test.go":             "b54892061ef0e5585251b946495df4b1d5e1690df1b69fef3b4df2fc29301c09",
	"internal/releaseartifacts/release.go":            "ea7a839576a744ec99ca22394e4823491ea36c12fd118ecb3f8e74cb18d5ae3c",
	"internal/releaseartifacts/release_publish.go":    "e9e04148cc5ea4a0a656b2e09e11b3a4785aebed9817c8e3d42b0701c8868f34",
	"internal/releaseartifacts/release_test.go":       "794fe0026e80696d9a8ea5172bcf8d8790e0afd9afb07ec4a4146cd4ff698340",
	"internal/scopesiftermcp/server_test.go":          "e350fa880009a62e9e888e0f6ed812bd12ff0a8cc08791d86fbadaf4e8ab2347",
	"internal/workflowrunner/runner.go":               "9798ab60e1e7c3592447f4d57e3b141ba6c7c6cb648d7f310354c96741c55184",
	"internal/workflowrunner/runner_test.go":          "bb78ba1e7149e49e94e623e488a1e2d2e9006a8922a126f68ebe14f00d9b1402",
	"navigator/git_identity.go":                       "797550ea84ca03e1493d624b1ed0743261160fbc72719fa3410e4b9c32edbb8d",
	"navigator/git_revision.go":                       "a69f911aa8b5579e26679b70bd2a4eba3540bd0942fc7a3ec5fd3bacd5665a37",
	"navigator/git_revision_test.go":                  "94bfdcf15d4c48407c717bc476c6aa35c35e7878aa4c3f3e13e52c739f61d96d",
	"navigator/navigate.go":                           "bf5fe673697f0a87fb20d1e7e128d79d2867ab3d9069957f2e1f7c779b4da2c9",
	"navigator/navigate_test.go":                      "71c3b1fd69a06c4d853f2985df166a7472dc3f96b18e559c7c6c8e2e59e97bb4",
	"navigator/testmain_test.go":                      "b0f01f0f518c28ba09ee6f10faeae897a33027a5de920a4e340e4af4f68ac181",
}
