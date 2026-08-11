package workspace

// PrepareRequest names one absent private workspace root and the finite limits
// shared by both arms. Mount layout and policy are code-owned.
type PrepareRequest struct {
	Root   string
	Limits Limits
}

// ArmPaths is the closed model-visible filesystem surface for one fresh arm.
// It is audit data; only a live ArmAuthority can authorize its use.
type ArmPaths struct {
	ModelRoot string `json:"model_root"`
	CacheRoot string `json:"cache_root"`
}
