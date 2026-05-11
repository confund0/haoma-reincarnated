package ipcclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/ipc"
)

type Config struct {
	FrontendDir string

	Addr string

	ClientName string

	ClientVersion string

	ReconnectInitial time.Duration

	ReconnectMax time.Duration
}

type Client struct {
	cfg         Config
	tlsCfg      *tls.Config
	bearer      string
	incoming    chan ipc.Frame
	outgoing    chan ipc.Frame
	connChanges chan bool

	mu        sync.Mutex
	conn      *websocket.Conn
	closed    bool
	connected bool

	ctx    context.Context
	cancel context.CancelFunc
}

func New(cfg Config) (*Client, error) {
	if cfg.FrontendDir == "" {
		return nil, errors.New("ipcclient: FrontendDir required")
	}
	if cfg.Addr == "" {
		return nil, errors.New("ipcclient: Addr required")
	}
	if cfg.ClientName == "" {
		cfg.ClientName = "unknown-client"
	}
	if cfg.ReconnectInitial == 0 {
		cfg.ReconnectInitial = 500 * time.Millisecond
	}
	if cfg.ReconnectMax == 0 {
		cfg.ReconnectMax = 30 * time.Second
	}

	certPEM, err := os.ReadFile(filepath.Join(cfg.FrontendDir, "cert.pem"))
	if err != nil {
		return nil, fmt.Errorf("ipcclient: read daemon cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		return nil, errors.New("ipcclient: parse daemon cert.pem")
	}
	tlsCfg := &tls.Config{
		RootCAs:    pool,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}

	token, err := ipc.ReadSensitive(ipc.TokenPath(cfg.FrontendDir))
	if err != nil {
		return nil, fmt.Errorf("ipcclient: read daemon token: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		cfg:         cfg,
		tlsCfg:      tlsCfg,
		bearer:      token,
		incoming:    make(chan ipc.Frame, 64),
		outgoing:    make(chan ipc.Frame, 64),
		connChanges: make(chan bool, 16),
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

func (c *Client) Run() error {

	defer func() {
		close(c.incoming)
		close(c.connChanges)
	}()
	backoff := c.cfg.ReconnectInitial
	for {
		if err := c.ctx.Err(); err != nil {
			return nil
		}
		wasConnected := c.runOnce()
		if c.isClosed() {
			return nil
		}
		if wasConnected {
			backoff = c.cfg.ReconnectInitial
		}

		select {
		case <-c.ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > c.cfg.ReconnectMax {
			backoff = c.cfg.ReconnectMax
		}
	}
}

func (c *Client) runOnce() (wasConnected bool) {
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: c.tlsCfg},
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+c.bearer)

	url := "wss://" + c.cfg.Addr + "/ws"
	conn, _, err := websocket.Dial(c.ctx, url, &websocket.DialOptions{
		HTTPClient: httpClient,
		HTTPHeader: hdr,
	})
	if err != nil {
		return false
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		wasUp := c.connected
		c.connected = false
		c.mu.Unlock()
		if wasUp {
			c.emitConnState(false)
		}
		conn.Close(websocket.StatusNormalClosure, "")
	}()

	hello, err := ipc.NewFrame(ipc.FrameHello, "handshake", ipc.HelloPayload{
		ClientName:    c.cfg.ClientName,
		ClientVersion: c.cfg.ClientVersion,
	})
	if err != nil {
		return false
	}
	if err := writeFrame(c.ctx, conn, hello); err != nil {
		return false
	}

	hsCtx, hsCancel := context.WithTimeout(c.ctx, 10*time.Second)
	welcome, err := readFrame(hsCtx, conn)
	hsCancel()
	if err != nil {
		return false
	}
	if welcome.Type != ipc.FrameWelcome {
		return false
	}

	select {
	case c.incoming <- welcome:
	case <-c.ctx.Done():
		return false
	}

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	c.emitConnState(true)
	wasConnected = true

	readErr := make(chan error, 1)
	go func() {
		for {
			f, err := readFrame(c.ctx, conn)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case c.incoming <- f:
			case <-c.ctx.Done():
				readErr <- c.ctx.Err()
				return
			}
		}
	}()

	waitReader := func() { <-readErr }

	for {
		select {
		case <-c.ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			waitReader()
			return wasConnected
		case <-readErr:
			return wasConnected
		case f := <-c.outgoing:
			if err := writeFrame(c.ctx, conn, f); err != nil {
				conn.Close(websocket.StatusNormalClosure, "")
				waitReader()
				return wasConnected
			}
		}
	}
}

func (c *Client) Send(f ipc.Frame) {
	select {
	case c.outgoing <- f:
	default:
	}
}

func (c *Client) Incoming() <-chan ipc.Frame { return c.incoming }

func (c *Client) Connection() <-chan bool { return c.connChanges }

func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *Client) emitConnState(up bool) {
	select {
	case c.connChanges <- up:
	default:
	}
}

func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.cancel()
}

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func writeFrame(ctx context.Context, conn *websocket.Conn, f ipc.Frame) error {
	b, err := ipc.Encode(f)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func readFrame(ctx context.Context, conn *websocket.Conn) (ipc.Frame, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return ipc.Frame{}, err
	}
	return ipc.Decode(data)
}
