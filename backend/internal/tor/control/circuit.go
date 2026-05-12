package control

import (
	"errors"
	"fmt"
	"strings"
)

type Circuit struct {
	ID         string
	Status     string
	Path       string
	BuildFlags string
	Purpose    string
	HSAddress  string
}

func (c *Conn) CircuitStatus() ([]Circuit, error) {
	raw, err := c.GetInfo("circuit-status")
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var out []Circuit
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		ci, err := parseCircuitLine(line)
		if err != nil {
			return nil, fmt.Errorf("control: parse circuit-status line %q: %w", line, err)
		}
		out = append(out, ci)
	}
	return out, nil
}

func parseCircuitLine(line string) (Circuit, error) {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return Circuit{}, errors.New("too few fields")
	}
	ci := Circuit{ID: parts[0], Status: parts[1]}
	i := 2

	if i < len(parts) && strings.HasPrefix(parts[i], "$") {
		ci.Path = parts[i]
		i++
	}
	for ; i < len(parts); i++ {
		eq := strings.IndexByte(parts[i], '=')
		if eq <= 0 {
			continue
		}
		key, val := parts[i][:eq], parts[i][eq+1:]
		switch key {
		case "BUILD_FLAGS":
			ci.BuildFlags = val
		case "PURPOSE":
			ci.Purpose = val
		case "REND_QUERY":
			ci.HSAddress = val
		}
	}
	return ci, nil
}

func (c *Conn) HsFetch(serviceID string) error {
	if serviceID == "" {
		return errors.New("control: empty serviceID")
	}
	reply, err := c.cmd("HSFETCH " + serviceID)
	if err != nil {
		return err
	}
	if reply.Code != 250 {
		return fmt.Errorf("control: HSFETCH: %d %s", reply.Code, strings.Join(reply.Lines, " "))
	}
	return nil
}

func (c *Conn) CloseCircuit(id string) error {
	if id == "" {
		return errors.New("control: empty circuit id")
	}
	reply, err := c.cmd("CLOSECIRCUIT " + id)
	if err != nil {
		return err
	}
	switch reply.Code {
	case 250:
		return nil
	case 552:
		return nil
	default:
		return fmt.Errorf("control: CLOSECIRCUIT: %d %s", reply.Code, strings.Join(reply.Lines, " "))
	}
}
