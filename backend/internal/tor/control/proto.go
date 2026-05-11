package control

import (
	"errors"
	"fmt"
	"strings"
)

type ProtocolInfo struct {
	Methods    []string
	CookieFile string
	Version    string
}

func (p *ProtocolInfo) Has(method string) bool {
	for _, m := range p.Methods {
		if m == method {
			return true
		}
	}
	return false
}

func (c *Conn) ProtocolInfo() (*ProtocolInfo, error) {
	reply, err := c.cmd("PROTOCOLINFO 1")
	if err != nil {
		return nil, err
	}
	if reply.Code != 250 {
		return nil, fmt.Errorf("control: PROTOCOLINFO: %d %s", reply.Code, strings.Join(reply.Lines, " "))
	}
	return parseProtocolInfo(reply.Lines)
}

func parseProtocolInfo(lines []string) (*ProtocolInfo, error) {
	p := &ProtocolInfo{}
	for _, line := range lines {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		kind := line[:sp]
		rest := line[sp+1:]
		switch kind {
		case "AUTH":
			pairs, err := tokenKV(rest)
			if err != nil {
				return nil, err
			}
			if m, ok := pairs["METHODS"]; ok {
				p.Methods = strings.Split(m, ",")
			}
			if f, ok := pairs["COOKIEFILE"]; ok {
				p.CookieFile = f
			}
		case "VERSION":
			pairs, err := tokenKV(rest)
			if err != nil {
				return nil, err
			}
			if v, ok := pairs["Tor"]; ok {
				p.Version = v
			}
		}
	}
	if len(p.Methods) == 0 {
		return nil, errors.New("control: PROTOCOLINFO reply missing AUTH METHODS")
	}
	return p, nil
}

func tokenKV(s string) (map[string]string, error) {
	out := map[string]string{}
	for len(s) > 0 {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			break
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, fmt.Errorf("control: token without =: %q", s)
		}
		key := s[:eq]
		s = s[eq+1:]
		var val string
		if strings.HasPrefix(s, `"`) {
			s = s[1:]
			end := strings.IndexByte(s, '"')
			if end < 0 {
				return nil, errors.New("control: unterminated quoted value")
			}
			val = s[:end]
			s = s[end+1:]
		} else {
			if end := strings.IndexByte(s, ' '); end < 0 {
				val = s
				s = ""
			} else {
				val = s[:end]
				s = s[end:]
			}
		}
		out[key] = val
	}
	return out, nil
}
