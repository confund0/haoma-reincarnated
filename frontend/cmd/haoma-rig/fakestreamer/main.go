package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	os.Exit(runMain())
}

func runMain() int {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	signal.Ignore(syscall.SIGPIPE)
	parentGone := make(chan struct{})
	go watchParent(parentGone)
	stop := make(chan struct{})
	go func() {
		select {
		case <-sig:
		case <-parentGone:
		}
		close(stop)
	}()
	return run(os.Args, os.Stdin, os.Stdout, os.Stderr, stop)
}

func watchParent(gone chan<- struct{}) {
	first := os.Getppid()
	for {
		time.Sleep(time.Second)
		if os.Getppid() != first {
			close(gone)
			return
		}
	}
}
