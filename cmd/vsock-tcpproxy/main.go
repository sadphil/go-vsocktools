// Command vsock-tcpproxy accepts connections on a virtio vsock port and
// forwards each of them to a TCP destination.
//
// Usage:
//
//	vsock-tcpproxy -port 5000 -target 127.0.0.1:3389 [-allow-cid 2] [-keepalive] [-v]
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/sadphil/go-winvsocktools/internal/logging"
	"github.com/sadphil/go-winvsocktools/internal/relay"
	"github.com/sadphil/go-winvsocktools/internal/server"
	"github.com/sadphil/go-winvsocktools/internal/tcpopt"
	"github.com/sadphil/go-winvsocktools/internal/version"
	"github.com/sadphil/go-winvsocktools/internal/vsock"
)

const program = "vsock-tcpproxy"

type options struct {
	target         string
	connectTimeout time.Duration
	tcp            tcpopt.Options
}

func main() {
	var cfg server.Config
	var opt options
	fs := flag.NewFlagSet(program, flag.ExitOnError)
	cfg.RegisterFlags(fs)
	fs.StringVar(&opt.target, "target", "", "TCP destination `host:port` (required)")
	fs.DurationVar(&opt.connectTimeout, "connect-timeout", 10*time.Second, "TCP connect timeout")
	opt.tcp.RegisterFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s -port PORT -target HOST:PORT [options]\n\n"+
			"Forwards every connection accepted on a vsock port to a TCP destination.\n\nOptions:\n", program)
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])
	if cfg.Version {
		fmt.Println(program, version.Version)
		return
	}

	log := logging.New(os.Stderr, program, cfg.Level())
	if err := cfg.Validate(); err != nil {
		log.Fatalf("%v", err)
	}
	if opt.target == "" {
		log.Fatalf("-target is required")
	}
	if _, _, err := net.SplitHostPort(opt.target); err != nil {
		log.Fatalf("invalid -target %q: %v", opt.target, err)
	}

	dialer := opt.tcp.Dialer(opt.connectTimeout)
	log.Infof("forwarding to tcp %s with %s", opt.target, &opt.tcp)

	err := server.Run(log, &cfg, func(log *logging.Logger, vc *vsock.Conn) {
		proxy(log, dialer, &opt, vc)
	})
	if err != nil {
		log.Fatalf("%v", err)
	}
}

func proxy(log *logging.Logger, dialer *net.Dialer, opt *options, vc *vsock.Conn) {
	log.Debugf("connecting to tcp %s", opt.target)
	c, err := dialer.Dial("tcp", opt.target)
	if err != nil {
		log.Errorf("connect to %s: %v", opt.target, err)
		vc.Close()
		return
	}
	tc := c.(*net.TCPConn)
	if err := opt.tcp.Apply(tc); err != nil {
		log.Warnf("%v", err)
	}
	log.Infof("connected to tcp %s from %s, relaying", tc.RemoteAddr(), tc.LocalAddr())

	res := relay.Join(log, "vsock", vc, "tcp", tc)
	if res.Err != nil {
		log.Warnf("relay aborted: %v", res.Err)
	}
	log.Infof("closed: vsock->tcp %d bytes, tcp->vsock %d bytes", res.AtoB, res.BtoA)
}
