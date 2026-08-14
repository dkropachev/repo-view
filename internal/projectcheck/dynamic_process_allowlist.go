package projectcheck

// approvedDynamicProcessFileSHA256 binds complete reviewed source files that
// must invoke an authenticated or runtime-selected native executable. Any byte
// change invalidates the exception and requires another review; new files fail
// closed. Explicit script-runtime names remain forbidden even in these files.
var approvedDynamicProcessFileSHA256 = map[string]string{
	"cmd/scopesifter-codex/main_test.go":              "590f9139652005d468a4a39c80f7a38d6d8ffee51d40bd4a81382e95df1860d6",
	"cmd/scopesifter-codex/signal_unix_test.go":       "ea182d1cb5ee4c0cee9edd0d28fdd622a6b1d45d5593e3eb06e4872f7cd4679e",
	"cmd/scopesifter-validate/main.go":                "6d56b6f6ca67294211f9ed33b9b6c7343faa61a68bdfdff0e7211f8aa487be3b",
	"cmd/scopesifter-validate/main_test.go":           "5e90fdae3873bdff07019ca15afcece3bf64659805f40a916201586ec020b3ce",
	"cmd/scopesifter/main_test.go":                    "5963ad93759ae769f6efc5d40e18bf7db9de82a36c7d1c750ed98fa11590e32e",
	"cmd/scopesifter/mcp_test.go":                     "bb8100bcbd2db4e7b1a633f783f096e5bfc78579237992892a6dd8f848e0b5bb",
	"internal/codexlauncher/exit_status_unix_test.go": "25dbe0e1bd3f9ba45d5e4c14cc7fbce880b8b77bc11d76ccc0a2d5fa4977cfe6",
	"internal/codexlauncher/launcher.go":              "b8801189956a54769404dcf548cb16814993626c3f5d0446464345b1e747f0d0",
	"internal/grammargen/runner.go":                   "3a63d6b844d541c1a6064de0bb369a341687e8e1e53e75eb4636085b4ca38e34",
	"internal/grammargen/runner_test.go":              "4163aaae35594de3ecd13ee4c9379ac283d4da722e7ca00ab98ee1d6ad2b58b6",
	"internal/processpolicy/native.go":                "a250c2408038b936ee33f4325d301f6ee6146d217a446fc69c36d874223f1a31",
	"internal/projectcheck/check.go":                  "33483263cd846361c6de94440c905e203c9231eacd8dde466fba43d035eb3935",
	"internal/projectcheck/check_test.go":             "b54892061ef0e5585251b946495df4b1d5e1690df1b69fef3b4df2fc29301c09",
	"internal/releaseartifacts/release.go":            "ea7a839576a744ec99ca22394e4823491ea36c12fd118ecb3f8e74cb18d5ae3c",
	"internal/releaseartifacts/release_publish.go":    "e9e04148cc5ea4a0a656b2e09e11b3a4785aebed9817c8e3d42b0701c8868f34",
	"internal/releaseartifacts/release_test.go":       "794fe0026e80696d9a8ea5172bcf8d8790e0afd9afb07ec4a4146cd4ff698340",
	"internal/scopesiftermcp/server_test.go":          "9291ba3c0342a6f4daa9db3f489ec5933b55b007fe5f62fd3666d3f38e23153c",
	"internal/workflowrunner/runner.go":               "9798ab60e1e7c3592447f4d57e3b141ba6c7c6cb648d7f310354c96741c55184",
	"internal/workflowrunner/runner_test.go":          "bb78ba1e7149e49e94e623e488a1e2d2e9006a8922a126f68ebe14f00d9b1402",
	"navigator/git_identity.go":                       "797550ea84ca03e1493d624b1ed0743261160fbc72719fa3410e4b9c32edbb8d",
	"navigator/git_revision.go":                       "a69f911aa8b5579e26679b70bd2a4eba3540bd0942fc7a3ec5fd3bacd5665a37",
	"navigator/git_revision_test.go":                  "94bfdcf15d4c48407c717bc476c6aa35c35e7878aa4c3f3e13e52c739f61d96d",
	"navigator/navigate.go":                           "dd0c25876c3947f0803fdf6a3ee0418c6cd0fe0a456386de325ed2c09930234f",
	"navigator/navigate_test.go":                      "1c871ed6df578dcd3dc2facbb203610c45085597a1bd9ad4eaeef648ad3b3adf",
	"navigator/testmain_test.go":                      "b20f58c3fc1ebab336d83151027911b98e963fe4d0c7c7d231d64062c0b008fa",
}
