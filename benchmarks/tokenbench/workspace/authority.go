package workspace

// PrepareRequest names one existing empty mountpoint and the finite limits
// shared by both arms. Prepare borrows Root, retains its exact directory
// descriptor, and never creates, removes, or renames it. Mount layout and
// policy are code-owned.
type PrepareRequest struct {
	Root   string
	Limits Limits
}

// ArmPaths is the closed model-visible filesystem surface for one fresh arm.
// ModelRoot changes and CacheRoot entries share the committed MaximumEntries
// writable-inode budget. This is audit data; only a live ArmAuthority can
// authorize its use.
type ArmPaths struct {
	ModelRoot string `json:"model_root"`
	CacheRoot string `json:"cache_root"`
}
