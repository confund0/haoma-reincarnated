package devicetag

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

var key = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("devicetag: no random source: " + err.Error())
	}
	return k
}()

func Tag(id uint32) string {
	if id == 0 {
		return ""
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], id)
	m := hmac.New(sha256.New, key)
	m.Write(b[:])
	return hex.EncodeToString(m.Sum(nil)[:4])
}
