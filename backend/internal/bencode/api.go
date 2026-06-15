// Vendored from github.com/anacrolix/torrent/bencode at commit
// c20f73d53e9f. Licensed under Mozilla Public License 2.0; see
// LICENSE in this directory. See docs/DEP-BENCODE-VENDOR.md for
// rationale. One mechanical edit vs upstream: MustMarshal's
// expect.Nil(err) inlined as panic-on-non-nil to drop the
// missinggo/expect transitive.

package bencode

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
)

type MarshalTypeError struct {
	Type reflect.Type
}

func (e *MarshalTypeError) Error() string {
	return "bencode: unsupported type: " + e.Type.String()
}

type UnmarshalInvalidArgError struct {
	Type reflect.Type
}

func (e *UnmarshalInvalidArgError) Error() string {
	if e.Type == nil {
		return "bencode: Unmarshal(nil)"
	}

	if e.Type.Kind() != reflect.Ptr {
		return "bencode: Unmarshal(non-pointer " + e.Type.String() + ")"
	}
	return "bencode: Unmarshal(nil " + e.Type.String() + ")"
}

type UnmarshalTypeError struct {
	BencodeTypeName     string
	UnmarshalTargetType reflect.Type
}

func (e *UnmarshalTypeError) Error() string {
	return fmt.Sprintf(
		"can't unmarshal a bencode %v into a %v",
		e.BencodeTypeName,
		e.UnmarshalTargetType,
	)
}

type UnmarshalFieldError struct {
	Key   string
	Type  reflect.Type
	Field reflect.StructField
}

func (e *UnmarshalFieldError) Error() string {
	return "bencode: key \"" + e.Key + "\" led to an unexported field \"" +
		e.Field.Name + "\" in type: " + e.Type.String()
}

type SyntaxError struct {
	Offset int64
	What   error
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("bencode: syntax error (offset: %d): %s", e.Offset, e.What)
}

type MarshalerError struct {
	Type reflect.Type
	Err  error
}

func (e *MarshalerError) Error() string {
	return "bencode: error calling MarshalBencode for type " + e.Type.String() + ": " + e.Err.Error()
}

type UnmarshalerError struct {
	Type reflect.Type
	Err  error
}

func (e *UnmarshalerError) Error() string {
	return "bencode: error calling UnmarshalBencode for type " + e.Type.String() + ": " + e.Err.Error()
}

type Marshaler interface {
	MarshalBencode() ([]byte, error)
}

type Unmarshaler interface {
	UnmarshalBencode([]byte) error
}

func Marshal(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	e := Encoder{w: &buf}
	err := e.Encode(v)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func MustMarshal(v interface{}) []byte {
	b, err := Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func Unmarshal(data []byte, v interface{}) (err error) {
	buf := bytes.NewReader(data)
	e := Decoder{r: buf}
	err = e.Decode(v)
	if err == nil && buf.Len() != 0 {
		err = ErrUnusedTrailingBytes{buf.Len()}
	}
	return
}

type ErrUnusedTrailingBytes struct {
	NumUnusedBytes int
}

func (me ErrUnusedTrailingBytes) Error() string {
	return fmt.Sprintf("%d unused trailing bytes", me.NumUnusedBytes)
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: &scanner{r: r}}
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}
