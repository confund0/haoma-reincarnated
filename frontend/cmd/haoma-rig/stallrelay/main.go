package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdin, os.Stderr))
}

func runMain(args []string, stdin io.Reader, stderr io.Writer) int {
	fs := flag.NewFlagSet("stallrelay", flag.ContinueOnError)
	micPort := fs.Int("mic-port", 15551, "TCP port haoma-mic listens on")
	spkPort := fs.Int("spk-port", 15552, "TCP port haoma-spk listens on")
	stallEvery := fs.Duration("stall-every", 30*time.Second, "how often a stall starts on its own; 0 disables the periodic schedule")
	stallFor := fs.Duration("stall-for", 5*time.Second, "how long each stall lasts before the capped drain starts")
	capBps := fs.Int64("cap", 5000, "bytes/sec the post-stall drain is capped at")
	bufBytes := fs.Int("buf", 2*1024*1024, "backlog buffer size in bytes; oldest bytes drop first when it fills")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	logf := func(format string, a ...interface{}) {
		fmt.Fprintf(stderr, format+"\n", a...)
	}

	micConn, err := dial(*micPort)
	if err != nil {
		logf("stallrelay: dial mic:%d: %v", *micPort, err)
		return 1
	}
	defer micConn.Close()
	spkConn, err := dial(*spkPort)
	if err != nil {
		logf("stallrelay: dial spk:%d: %v", *spkPort, err)
		return 1
	}
	defer spkConn.Close()
	logf("stallrelay: ferrying  mic:%d  ->  spk:%d", *micPort, *spkPort)

	stop := make(chan struct{})
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		close(stop)
	}()

	ctl := newStallController(*stallEvery, *stallFor, *capBps, *bufBytes, time.Now(), logf)

	go readTriggers(stdin, ctl, stop)

	return relay(micConn, spkConn, ctl, stop, logf)
}

func dial(port int) (net.Conn, error) {
	return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func readTriggers(stdin io.Reader, ctl *stallController, stop <-chan struct{}) {
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		select {
		case <-stop:
			return
		default:
		}
		ctl.Trigger(time.Now())
	}
}

func relay(mic, spk net.Conn, ctl *stallController, stop <-chan struct{}, logf func(string, ...interface{})) int {
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-stop:
			return 0
		default:
		}

		_ = mic.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := mic.Read(buf)
		now := time.Now()
		var out []byte
		if n > 0 {
			out = ctl.Submit(now, buf[:n])
		} else {
			out = ctl.Submit(now, nil)
		}
		if len(out) > 0 {
			if _, werr := spk.Write(out); werr != nil {
				logf("stallrelay: write to spk: %v", werr)
				return 1
			}
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if err == io.EOF {
				logf("stallrelay: mic closed")
				return 0
			}
			logf("stallrelay: read from mic: %v", err)
			return 1
		}
	}
}
