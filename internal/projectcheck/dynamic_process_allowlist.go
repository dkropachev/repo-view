package projectcheck

// approvedDynamicProcessFileSHA256 binds complete reviewed source files that
// must invoke an authenticated or runtime-selected native executable. Any byte
// change invalidates the exception and requires another review; new files fail
// closed. Explicit script-runtime names remain forbidden even in these files.
var approvedDynamicProcessFileSHA256 = map[string]string{
	"cmd/scopesifter-validate/main.go":             "6d56b6f6ca67294211f9ed33b9b6c7343faa61a68bdfdff0e7211f8aa487be3b",
	"cmd/scopesifter-validate/main_test.go":        "5e90fdae3873bdff07019ca15afcece3bf64659805f40a916201586ec020b3ce",
	"cmd/scopesifter/main_test.go":                 "ab590dc1c600c532fdea9330a8cec0bef3696768aa0b8c2769e67876662229d2",
	"internal/grammargen/runner.go":                "3a63d6b844d541c1a6064de0bb369a341687e8e1e53e75eb4636085b4ca38e34",
	"internal/grammargen/runner_test.go":           "4163aaae35594de3ecd13ee4c9379ac283d4da722e7ca00ab98ee1d6ad2b58b6",
	"internal/processpolicy/native.go":             "a250c2408038b936ee33f4325d301f6ee6146d217a446fc69c36d874223f1a31",
	"internal/projectcheck/check.go":               "33483263cd846361c6de94440c905e203c9231eacd8dde466fba43d035eb3935",
	"internal/projectcheck/check_test.go":          "b54892061ef0e5585251b946495df4b1d5e1690df1b69fef3b4df2fc29301c09",
	"internal/releaseartifacts/release.go":         "ea7a839576a744ec99ca22394e4823491ea36c12fd118ecb3f8e74cb18d5ae3c",
	"internal/releaseartifacts/release_publish.go": "e9e04148cc5ea4a0a656b2e09e11b3a4785aebed9817c8e3d42b0701c8868f34",
	"internal/releaseartifacts/release_test.go":    "794fe0026e80696d9a8ea5172bcf8d8790e0afd9afb07ec4a4146cd4ff698340",
	"internal/workflowrunner/runner.go":            "9798ab60e1e7c3592447f4d57e3b141ba6c7c6cb648d7f310354c96741c55184",
	"internal/workflowrunner/runner_test.go":       "bb78ba1e7149e49e94e623e488a1e2d2e9006a8922a126f68ebe14f00d9b1402",
	"navigator/git_identity.go":                    "797550ea84ca03e1493d624b1ed0743261160fbc72719fa3410e4b9c32edbb8d",
	"navigator/git_revision.go":                    "a69f911aa8b5579e26679b70bd2a4eba3540bd0942fc7a3ec5fd3bacd5665a37",
	"navigator/git_revision_test.go":               "94bfdcf15d4c48407c717bc476c6aa35c35e7878aa4c3f3e13e52c739f61d96d",
	"navigator/navigate.go":                        "bd84ae822137f2252594cf86f6cf346bf136b60eac87816273dc50eba1a7a906",
	"navigator/navigate_test.go":                   "e346a1c435a43e9b76341caae124d33f742963c02ae27a0ccbb3ec9b3be8389d",
	"navigator/testmain_test.go":                   "b20f58c3fc1ebab336d83151027911b98e963fe4d0c7c7d231d64062c0b008fa",
}
