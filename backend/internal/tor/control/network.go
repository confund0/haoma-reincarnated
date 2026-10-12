package control

import (
	"errors"
	"fmt"
	"strings"
)

func (c *Conn) SocksListeners() ([]string, error) {
	raw, err := c.GetInfo("net/listeners/socks")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range strings.Fields(raw) {
		if f = strings.Trim(f, `"`); f != "" {
			out = append(out, f)
		}
	}
	return out, nil
}

func isAutoPort(addr string) bool {
	return strings.EqualFold(addr, "auto") || strings.HasSuffix(strings.ToLower(addr), ":auto")
}

func (c *Conn) PinSocksPort() (orig, pinned string, err error) {
	lines, err := c.GetConf("SocksPort")
	if err != nil {
		return "", "", err
	}
	auto := 0
	for _, l := range lines {
		addr, _, _ := strings.Cut(strings.TrimSpace(l), " ")
		if isAutoPort(addr) {
			auto++
		}
	}
	if auto == 0 {
		return "", "", nil
	}
	if len(lines) != 1 {
		return "", "", fmt.Errorf("control: %d SocksPort lines, %d of them on an auto port; tor does not say which listener is whose", len(lines), auto)
	}
	listeners, err := c.SocksListeners()
	if err != nil {
		return "", "", err
	}
	if len(listeners) != 1 {
		return "", "", fmt.Errorf("control: one SocksPort line on an auto port, but %d SOCKS listeners", len(listeners))
	}
	_, flags, _ := strings.Cut(strings.TrimSpace(lines[0]), " ")
	line := listeners[0]
	if flags = strings.TrimSpace(flags); flags != "" {
		line += " " + flags
	}
	if err := c.SetConf("SocksPort", line); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(lines[0]), line, nil
}

func (c *Conn) EnableNetworkUnpinned(socksLine string) error {
	if strings.ContainsAny(socksLine, "\r\n") {
		return errors.New("control: SocksPort line holds a line break")
	}
	reply, err := c.cmd("SETCONF SocksPort=" + quoteConf(socksLine) + " DisableNetwork=" + quoteConf("0"))
	if err != nil {
		return err
	}
	if reply.Code != 250 {
		return &StatusError{Command: "SETCONF", Code: reply.Code, Lines: reply.Lines}
	}
	return nil
}

func (c *Conn) ToggleNetwork() (off bool, err error) {
	if err := c.SetConf("DisableNetwork", "1"); err != nil {

		var se *StatusError
		return !errors.As(err, &se), err
	}
	if err := c.SetConf("DisableNetwork", "0"); err != nil {
		if err2 := c.SetConf("DisableNetwork", "0"); err2 != nil {
			return true, fmt.Errorf("control: DisableNetwork=0 failed twice: %w; %w", err, err2)
		}
	}
	return false, nil
}
