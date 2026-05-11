package control

import (
	"fmt"
	"strings"
)

func (c *Conn) GetInfo(key string) (string, error) {
	reply, err := c.cmd("GETINFO " + key)
	if err != nil {
		return "", err
	}
	if reply.Code != 250 {
		return "", fmt.Errorf("control: GETINFO: %d %s", reply.Code, strings.Join(reply.Lines, " "))
	}
	if len(reply.Lines) < 2 {
		return "", fmt.Errorf("control: short GETINFO reply: %v", reply.Lines)
	}
	first := reply.Lines[0]
	eq := strings.IndexByte(first, '=')
	if eq < 0 {
		return "", fmt.Errorf("control: no = in GETINFO reply: %q", first)
	}
	if inline := first[eq+1:]; inline != "" {
		return inline, nil
	}

	return strings.Join(reply.Lines[1:len(reply.Lines)-1], "\n"), nil
}
