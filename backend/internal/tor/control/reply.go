package control

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

type Reply struct {
	Code  int
	Lines []string
}

type Event struct {
	Type  string
	Lines []string
}

func readOne(r *bufio.Reader) (*Reply, *Event, error) {
	var code int
	var lines []string
	for {
		raw, err := r.ReadString('\n')
		if err != nil {
			return nil, nil, err
		}
		line := strings.TrimRight(raw, "\r\n")
		if len(line) < 4 {
			return nil, nil, fmt.Errorf("control: short line %q", line)
		}
		c, err := strconv.Atoi(line[:3])
		if err != nil {
			return nil, nil, fmt.Errorf("control: bad code %q: %v", line[:3], err)
		}
		sep := line[3]
		body := line[4:]
		if len(lines) == 0 {
			code = c
		}
		switch sep {
		case '-':
			lines = append(lines, body)
		case '+':
			lines = append(lines, body)
			for {
				raw, err := r.ReadString('\n')
				if err != nil {
					return nil, nil, err
				}
				s := strings.TrimRight(raw, "\r\n")
				if s == "." {
					break
				}
				lines = append(lines, s)
			}
		case ' ':
			lines = append(lines, body)
			if code >= 600 && code < 700 {
				return nil, &Event{Type: firstToken(lines[0]), Lines: lines}, nil
			}
			return &Reply{Code: code, Lines: lines}, nil, nil
		default:
			return nil, nil, fmt.Errorf("control: bad separator %q in %q", string(sep), line)
		}
	}
}

func firstToken(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}
