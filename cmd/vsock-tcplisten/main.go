// Command vsock-tcplisten accepts TCP connections and connects each of them
// to a virtio vsock endpoint given by CID and port.
//
// Usage:
//
//	vsock-tcplisten -listen 127.0.0.1:2222 -cid 2 -port 22 [-allow-ip 10.0.0.0/8] [-keepalive] [-v]
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/sadphil/go-winvsocktools/internal/logging"
	"github.com/sadphil/go-winvsocktools/internal/relay"
	"github.com/sadphil/go-winvsocktools/internal/server"
	"github.com/sadphil/go-winvsocktools/internal/tcpopt"
	"github.com/sadphil/go-winvsocktools/internal/version"
	"github.com/sadphil/go-winvsocktools/internal/vsock"
)

const program = "vsock-tcplisten"

type options struct {
	server.Common
	listen string
	cid    uint64
	port   uint64
	allow  tcpopt.PrefixList
	tcp    tcpopt.Options
}

func main() {
	var opt options
	fs := flag.NewFlagSet(program, flag.ExitOnError)
	fs.StringVar(&opt.listen, "listen", "", "TCP `address` to listen on, e.g. 127.0.0.1:2222 or :2222 (required)")
	fs.Uint64Var(&opt.cid, "cid", vsock.CIDHost, "vsock `CID` to connect to (2 is the host)")
	fs.Uint64Var(&opt.port, "port", 0, "vsock `port` to connect to (required)")
	fs.Var(&opt.allow, "allow-ip", "comma-separated client `addresses or CIDR prefixes` allowed to connect; may be repeated (default: any)")
	opt.tcp.RegisterFlags(fs)
	opt.Common.RegisterFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s -listen ADDR:PORT -port VSOCKPORT [-cid CID] [options]\n\n"+
			"Forwards every accepted TCP connection to the vsock endpoint CID:VSOCKPORT.\n\nOptions:\n", program)
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])
	if opt.Version {
		fmt.Println(program, version.Version)
		return
	}

	log := logging.New(os.Stderr, program, opt.Level())
	if opt.listen == "" {
		log.Fatalf("-listen is required")
	}
	if err := server.ValidatePort(opt.port); err != nil {
		log.Fatalf("%v", err)
	}
	if opt.cid >= vsock.CIDAny {
		log.Fatalf("-cid must be below %d", uint32(vsock.CIDAny))
	}
	af, err := opt.ResolveAF(log)
	if err != nil {
		log.Fatalf("%v", err)
	}
	ln, err := opt.tcp.ListenConfig().Listen(context.Background(), "tcp", opt.listen)
	if err != nil {
		log.Fatalf("%v", err)
	}
	target := vsock.Addr{CID: uint32(opt.cid), Port: uint32(opt.port)}
	allowed := "any"
	if len(opt.allow) > 0 {
		allowed = opt.allow.String()
	}
	log.Infof("%s listening on tcp %s with %s, allowed clients: %s, forwarding to vsock %s (af %d)",
		version.Version, ln.Addr(), &opt.tcp, allowed, target, af)

	err = server.Serve(log, server.Loop[net.Conn]{
		Accept: ln.Accept,
		Close:  ln.Close,
		Admit: func(c net.Conn) (string, string, error) {
			peer := c.RemoteAddr().(*net.TCPAddr).AddrPort()
			desc := fmt.Sprintf("%s on %s", peer, c.LocalAddr())
			if !opt.allow.Allowed(peer.Addr()) {
				return "tcp=" + peer.String(), desc, fmt.Errorf("address %s not allowed", peer.Addr().Unmap())
			}
			return "tcp=" + peer.String(), desc, nil
		},
		Handle: func(log *logging.Logger, c net.Conn) {
			forward(log, &opt, af, target, c.(*net.TCPConn))
		},
	})
	if err != nil {
		log.Fatalf("%v", err)
	}
}

func forward(log *logging.Logger, opt *options, af int, target vsock.Addr, tc *net.TCPConn) {
	if err := opt.tcp.Apply(tc); err != nil {
		log.Warnf("%v", err)
	}
	log.Debugf("connecting to vsock %s", target)
	vc, err := vsock.Dial(af, target.CID, target.Port)
	if err != nil {
		log.Errorf("connect to vsock %s: %v", target, err)
		tc.Close()
		return
	}
	log.Infof("connected to vsock %s from %s, relaying", target, vc.LocalAddr())

	res := relay.Join(log, "tcp", tc, "vsock", vc)
	if res.Err != nil {
		log.Warnf("relay aborted: %v", res.Err)
	}
	log.Infof("closed: tcp->vsock %d bytes, vsock->tcp %d bytes", res.AtoB, res.BtoA)
}
