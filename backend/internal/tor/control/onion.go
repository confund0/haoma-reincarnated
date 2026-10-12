package control

import (
	"errors"
	"fmt"
	"strings"
)

var ErrOnionAddressCollision = errors.New("control: onion address collision")

var ErrUnknownOnionService = errors.New("control: unknown onion service")

const (
	addOnionCollisionCode = 550
	delOnionUnknownCode   = 552
)

type OnionPort struct {
	VirtPort int
	Target   string
}

type Onion struct {
	ServiceID  string
	PrivateKey string
}

func (c *Conn) AddOnionNew(ports []OnionPort, flags ...string) (*Onion, error) {
	return c.addOnion("NEW:ED25519-V3", ports, flags, true)
}

func (c *Conn) AddOnion(privateKey string, ports []OnionPort, flags ...string) (*Onion, error) {
	if privateKey == "" {
		return nil, errors.New("control: empty private key")
	}
	return c.addOnion("ED25519-V3:"+privateKey, ports, flags, false)
}

func (c *Conn) addOnion(keyspec string, ports []OnionPort, flags []string, expectPK bool) (*Onion, error) {
	if len(ports) == 0 {
		return nil, errors.New("control: at least one port required")
	}
	var b strings.Builder
	b.WriteString("ADD_ONION ")
	b.WriteString(keyspec)
	if len(flags) > 0 {
		b.WriteString(" Flags=")
		b.WriteString(strings.Join(flags, ","))
	}
	for _, p := range ports {
		b.WriteByte(' ')
		if p.Target == "" {
			fmt.Fprintf(&b, "Port=%d", p.VirtPort)
		} else {
			fmt.Fprintf(&b, "Port=%d,%s", p.VirtPort, p.Target)
		}
	}
	reply, err := c.cmd(b.String())
	if err != nil {
		return nil, err
	}
	if reply.Code != 250 {
		se := &StatusError{Command: "ADD_ONION", Code: reply.Code, Lines: reply.Lines}
		if reply.Code == addOnionCollisionCode {
			return nil, fmt.Errorf("%w: %w", ErrOnionAddressCollision, se)
		}
		return nil, se
	}
	o := &Onion{}
	for _, line := range reply.Lines {
		switch {
		case strings.HasPrefix(line, "ServiceID="):
			o.ServiceID = strings.TrimPrefix(line, "ServiceID=")
		case strings.HasPrefix(line, "PrivateKey=ED25519-V3:"):
			o.PrivateKey = strings.TrimPrefix(line, "PrivateKey=ED25519-V3:")
		}
	}
	if o.ServiceID == "" {
		return nil, fmt.Errorf("control: ADD_ONION reply missing ServiceID: %v", reply.Lines)
	}
	if expectPK && o.PrivateKey == "" {
		return nil, fmt.Errorf("control: ADD_ONION reply missing PrivateKey: %v", reply.Lines)
	}
	return o, nil
}

func (c *Conn) DelOnion(serviceID string) error {
	if serviceID == "" {
		return errors.New("control: empty serviceID")
	}
	reply, err := c.cmd("DEL_ONION " + serviceID)
	if err != nil {
		return err
	}
	if reply.Code != 250 {
		se := &StatusError{Command: "DEL_ONION", Code: reply.Code, Lines: reply.Lines}
		if reply.Code == delOnionUnknownCode {
			return fmt.Errorf("%w: %w", ErrUnknownOnionService, se)
		}
		return se
	}
	return nil
}
