package control

import (
	"errors"
	"fmt"
	"strings"
)

func (c *Conn) GetConf(key string) ([]string, error) {
	if key == "" || strings.ContainsAny(key, " \r\n") {
		return nil, fmt.Errorf("control: bad GETCONF key %q", key)
	}
	reply, err := c.cmd("GETCONF " + key)
	if err != nil {
		return nil, err
	}
	if reply.Code != 250 {
		return nil, &StatusError{Command: "GETCONF", Code: reply.Code, Lines: reply.Lines}
	}
	var out []string
	for _, line := range reply.Lines {
		k, v, _ := strings.Cut(line, "=")
		if !strings.EqualFold(k, key) {
			return nil, fmt.Errorf("control: GETCONF %s: unexpected line %q", key, line)
		}
		out = append(out, unquoteConf(v))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("control: GETCONF %s: empty reply", key)
	}
	return out, nil
}

func (c *Conn) SetConf(key, value string) error {
	if key == "" || strings.ContainsAny(key, " =\r\n") {
		return fmt.Errorf("control: bad SETCONF key %q", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("control: SETCONF value holds a line break")
	}
	reply, err := c.cmd("SETCONF " + key + "=" + quoteConf(value))
	if err != nil {
		return err
	}
	if reply.Code != 250 {
		return &StatusError{Command: "SETCONF", Code: reply.Code, Lines: reply.Lines}
	}
	return nil
}

func quoteConf(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(v) + `"`
}

func unquoteConf(v string) string {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return v
	}
	r := strings.NewReplacer(`\\`, `\`, `\"`, `"`)
	return r.Replace(v[1 : len(v)-1])
}
