// Command vsock-inetd accepts connections on a virtio vsock port and runs a
// program for each of them with its standard input and output connected to
// the client, like inetd.
//
// Usage:
//
//	vsock-inetd -port 5000 [-allow-cid 2] [-v] -- C:\Windows\System32\cmd.exe /q
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sadphil/go-winvsocktools/internal/child"
	"github.com/sadphil/go-winvsocktools/internal/logging"
	"github.com/sadphil/go-winvsocktools/internal/server"
	"github.com/sadphil/go-winvsocktools/internal/version"
	"github.com/sadphil/go-winvsocktools/internal/vsock"
)

const program = "vsock-inetd"

func main() {
	var cfg server.Config
	var spec child.Spec
	fs := flag.NewFlagSet(program, flag.ExitOnError)
	cfg.RegisterFlags(fs)
	fs.BoolVar(&cfg.Wait, "wait", false, "inetd \"wait\" mode: serve one client at a time (default is \"nowait\")")
	spec.RegisterFlags(fs, program)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s -port PORT [options] [--] PROGRAM [ARGS...]\n\n"+
			"Runs PROGRAM for every connection accepted on a vsock port with its standard\n"+
			"input and output connected to the client. The child's environment gets\n"+
			"VSOCK_REMOTE_CID, VSOCK_REMOTE_PORT, VSOCK_LOCAL_CID and VSOCK_LOCAL_PORT.\n\nOptions:\n", program)
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
	if err := spec.Validate(); err != nil {
		log.Fatalf("%v", err)
	}
	if err := spec.SetProgram(fs.Args()); err != nil {
		log.Fatalf("%v; see -h", err)
	}
	log.Infof("serving %s", &spec)

	err := server.Run(log, &cfg, func(log *logging.Logger, c *vsock.Conn) {
		code, err := child.Run(log, &spec, c)
		if err != nil {
			log.Errorf("%v", err)
			return
		}
		log.Infof("child exited with code %d", code)
	})
	if err != nil {
		log.Fatalf("%v", err)
	}
}
