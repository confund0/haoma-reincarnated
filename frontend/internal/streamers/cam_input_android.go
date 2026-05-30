//go:build android

package streamers

func init() {
	platformCamExtras = []string{"--input-from-raw"}
}
