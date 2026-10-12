//go:build !faults

package main

import "net/http"

type stallFault struct{}

func readStallFault(func(string) string) *stallFault { return nil }

func (*stallFault) wrap(*daemon, string, string, *http.Client) {}
