// Vendored from github.com/anacrolix/torrent/bencode at commit
// c20f73d53e9f. MPL 2.0; see LICENSE in this directory.

package bencode

import (
	"errors"
	"fmt"
)

type Bytes []byte

var (
	_ Unmarshaler = (*Bytes)(nil)
	_ Marshaler   = (*Bytes)(nil)
	_ Marshaler   = Bytes{}
)

func (me *Bytes) UnmarshalBencode(b []byte) error {
	*me = append([]byte(nil), b...)
	return nil
}

func (me Bytes) MarshalBencode() ([]byte, error) {
	if len(me) == 0 {
		return nil, errors.New("marshalled Bytes should not be zero-length")
	}
	return me, nil
}

func (me Bytes) GoString() string {
	return fmt.Sprintf("bencode.Bytes(%q)", []byte(me))
}
