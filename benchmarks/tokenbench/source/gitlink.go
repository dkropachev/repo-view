package source

// gitlinkMaterialization is deliberately not part of the source digest. An
// absent gitlink and its conventional empty checkout directory represent the
// same opaque Git tree entry; this identity is retained only to detect races.
type gitlinkMaterialization struct {
	device    uint64
	inode     uint64
	linkCount uint64
	size      int64
	mtimeSec  int64
	mtimeNsec int64
	ctimeSec  int64
	ctimeNsec int64
	mode      uint32
	present   bool
}
