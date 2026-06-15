// Vendored from github.com/anacrolix/torrent/bencode at commit
// c20f73d53e9f. MPL 2.0; see LICENSE in this directory.

package bencode

import (
	"reflect"
	"unsafe"
)

var (
	marshalerType   = reflect.TypeOf((*Marshaler)(nil)).Elem()
	unmarshalerType = reflect.TypeOf((*Unmarshaler)(nil)).Elem()
)

func bytesAsString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}
