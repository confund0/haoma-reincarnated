//go:build !unix

package store

func checkNotInUse(badgerDir string) error { return nil }
