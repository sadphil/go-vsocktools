//go:build windows

package child

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sadphil/go-winvsocktools/internal/logging"
	"github.com/sadphil/go-winvsocktools/internal/vsock"
)

// runDirect starts the child with the socket handle itself as its standard
// input and output, waits for it to exit and returns its exit code.
//
// Only the handles named in PROC_THREAD_ATTRIBUTE_HANDLE_LIST are inherited,
// so children serving concurrent clients never receive each other's sockets.
func runDirect(log *logging.Logger, s *Spec, env []string, c *vsock.Conn) (int, error) {
	sock := windows.Handle(c.Fd())
	if err := windows.SetHandleInformation(sock, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return 0, fmt.Errorf("make socket inheritable: %w", err)
	}

	errh, err := childStderr(s.Stderr, sock)
	if err != nil {
		return 0, err
	}
	if errh != sock {
		defer windows.CloseHandle(errh)
	}
	handles := []windows.Handle{sock}
	if errh != sock {
		handles = append(handles, errh)
	}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, fmt.Errorf("NewProcThreadAttributeList: %w", err)
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return 0, fmt.Errorf("UpdateProcThreadAttribute: %w", err)
	}

	si := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  sock,
			StdOutput: sock,
			StdErr:    errh,
		},
		ProcThreadAttributeList: attrs.List(),
	}
	app, err := windows.UTF16PtrFromString(s.Path)
	if err != nil {
		return 0, err
	}
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(s.Args))
	if err != nil {
		return 0, err
	}
	envBlock, err := environBlock(env)
	if err != nil {
		return 0, err
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(app, cmdline, nil, nil, true,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		&envBlock[0], nil, &si.StartupInfo, &pi)
	runtime.KeepAlive(handles) // referenced from the attribute list
	if err != nil {
		return 0, fmt.Errorf("CreateProcess %s: %w", s.Path, err)
	}
	windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	log.Infof("started child pid %d", pi.ProcessId)

	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return 0, fmt.Errorf("wait for pid %d: %w", pi.ProcessId, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, fmt.Errorf("exit code of pid %d: %w", pi.ProcessId, err)
	}
	return int(int32(code)), nil
}

// childStderr returns an inheritable handle for the child's stderr. Unless it
// is sock itself, the caller must close it.
func childStderr(mode string, sock windows.Handle) (windows.Handle, error) {
	if mode == StderrSocket {
		return sock, nil
	}
	if mode == StderrInherit {
		if h := windows.Handle(os.Stderr.Fd()); h != 0 && h != windows.InvalidHandle {
			self := windows.CurrentProcess()
			var dup windows.Handle
			if err := windows.DuplicateHandle(self, h, self, &dup, 0, true, windows.DUPLICATE_SAME_ACCESS); err == nil {
				return dup, nil
			}
		}
		// No usable stderr (e.g. running as a service): fall back to NUL.
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	h, err := windows.CreateFile(windows.StringToUTF16Ptr("NUL"), windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, sa, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("open NUL: %w", err)
	}
	return h, nil
}

// environBlock builds a CREATE_UNICODE_ENVIRONMENT block from KEY=VALUE pairs.
func environBlock(env []string) ([]uint16, error) {
	var b []uint16
	for _, kv := range env {
		if strings.IndexByte(kv, 0) >= 0 {
			return nil, fmt.Errorf("environment variable contains NUL: %q", kv)
		}
		u, _ := windows.UTF16FromString(kv) // includes the terminating NUL
		b = append(b, u...)
	}
	return append(b, 0), nil
}
