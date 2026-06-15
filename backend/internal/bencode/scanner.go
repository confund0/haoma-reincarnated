// Vendored from github.com/anacrolix/torrent/bencode at commit
// c20f73d53e9f. MPL 2.0; see LICENSE in this directory.

package bencode

import (
	"errors"
	"io"
)

type scanner struct {
	r      io.Reader
	b      [1]byte
	unread bool
}

func (me *scanner) Read(b []byte) (int, error) {
	return me.r.Read(b)
}

func (me *scanner) ReadByte() (byte, error) {
	if me.unread {
		me.unread = false
		return me.b[0], nil
	}
	n, err := me.r.Read(me.b[:])
	if n == 1 {
		err = nil
	}
	return me.b[0], err
}

func (me *scanner) UnreadByte() error {
	if me.unread {
		return errors.New("byte already unread")
	}
	me.unread = true
	return nil
}
