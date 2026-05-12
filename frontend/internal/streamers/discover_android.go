//go:build android

package streamers

func init() {
	micBinaryName = "libhaoma-mic.so"
	spkBinaryName = "libhaoma-spk.so"
}
