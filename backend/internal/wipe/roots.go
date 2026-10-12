package wipe

import (
	"fmt"
	"path/filepath"
)

type Root struct {
	Path string
	Keep []string
}

const (
	androidFilesDir = "files"
	androidCacheDir = "cache"

	androidLibLink = "lib"
)

func Roots(cfgDir, goos string) ([]Root, error) {
	if goos != "android" {
		return []Root{{Path: cfgDir}}, nil
	}

	clean := filepath.Clean(cfgDir)
	files := filepath.Dir(clean)
	if filepath.Base(files) != androidFilesDir {
		return nil, fmt.Errorf("wipe: %s is not inside an app's files directory", clean)
	}
	data := filepath.Dir(files)

	return []Root{
		{Path: files},
		{Path: filepath.Join(data, androidCacheDir)},
		{Path: data, Keep: []string{androidFilesDir, androidCacheDir, androidLibLink}},
	}, nil
}
