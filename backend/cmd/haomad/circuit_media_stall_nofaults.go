//go:build !faults

package main

import (
	"io"

	"haoma/internal/proxy"
)

type mediaStallFault struct{}

func readMediaStallFault(func(string) string) *mediaStallFault { return nil }

func (*mediaStallFault) wrapServe(_ proxy.Modality, w io.Writer) io.Writer { return w }
