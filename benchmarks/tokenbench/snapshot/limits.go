package snapshot

const (
	maximumManifestEntries      = 100_000
	maximumSnapshotBytes        = int64(4 << 30)
	maximumRegularFileBytes     = int64(512 << 20)
	maximumPathBytes            = 4_096
	maximumPathDepth            = 128
	maximumLogicalOriginBytes   = 8_192
	maximumChangedFiles         = 20_000
	maximumChangedPatchBytes    = 64 << 20
	maximumPerFilePatchBytes    = 16 << 20
	maximumAggregatePatchBytes  = 128 << 20
	maximumChangedSpansPerFile  = 100_000
	maximumChangedLine          = 1_000_000_000
	maximumExpandedChangedLines = 100_000
	maximumHeadSubjectBytes     = 4_096
)
