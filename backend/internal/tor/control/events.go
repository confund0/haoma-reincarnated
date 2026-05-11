package control

import (
	"fmt"
	"strings"
)

func (c *Conn) SetEvents(events ...string) error {
	cmd := "SETEVENTS"
	if len(events) > 0 {
		cmd += " " + strings.Join(events, " ")
	}
	reply, err := c.cmd(cmd)
	if err != nil {
		return err
	}
	if reply.Code != 250 {
		return fmt.Errorf("control: SETEVENTS: %d %s", reply.Code, strings.Join(reply.Lines, " "))
	}
	return nil
}
