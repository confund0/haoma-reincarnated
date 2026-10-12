package files

func SetPutIfAbsentCheckpoint(t interface{ Cleanup(func()) }, fn func(msgID string)) {
	orig := putIfAbsentCheckpoint
	putIfAbsentCheckpoint = fn
	t.Cleanup(func() { putIfAbsentCheckpoint = orig })
}

func SetDeleteMetaCheckpoint(t interface{ Cleanup(func()) }, fn func(msgID string)) {
	orig := deleteMetaCheckpoint
	deleteMetaCheckpoint = fn
	t.Cleanup(func() { deleteMetaCheckpoint = orig })
}
