package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

const eventBuf = 64

type Conn struct {
	tcp net.Conn
	br  *bufio.Reader

	cmdMu   sync.Mutex
	replies chan Reply
	events  chan Event

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

func Dial(ctx context.Context, addr string) (*Conn, error) {
	var d net.Dialer
	tcp, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return newConn(tcp), nil
}

func newConn(tcp net.Conn) *Conn {
	c := &Conn{
		tcp:     tcp,
		br:      bufio.NewReader(tcp),
		replies: make(chan Reply, 1),
		events:  make(chan Event, eventBuf),
		closed:  make(chan struct{}),
	}
	go c.reader()
	return c
}

func (c *Conn) Events() <-chan Event {
	return c.events
}

func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.tcp.Close()
		close(c.closed)
	})
	return c.closeErr
}

func (c *Conn) cmd(line string) (Reply, error) {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()
	if _, err := fmt.Fprintf(c.tcp, "%s\r\n", line); err != nil {
		return Reply{}, err
	}
	select {
	case r, ok := <-c.replies:
		if !ok {
			return Reply{}, errors.New("control: connection closed")
		}
		return r, nil
	case <-c.closed:
		return Reply{}, errors.New("control: connection closed")
	}
}

func (c *Conn) reader() {
	defer close(c.replies)
	for {
		reply, event, err := readOne(c.br)
		if err != nil {
			c.Close()
			return
		}
		if event != nil {
			select {
			case c.events <- *event:
			default:

			}
			continue
		}
		select {
		case c.replies <- *reply:
		case <-c.closed:
			return
		}
	}
}
