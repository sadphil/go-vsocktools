# go-winvsocktools

Virtio vsock tools for Microsoft Windows guests running under KVM/QEMU.

| Program | What it does |
|---|---|
| `vsock-tcpproxy` | Accepts vsock connections and forwards each one to a TCP destination. |
| `vsock-tcplisten` | Accepts TCP connections and connects each one to a vsock endpoint (CID:port). |
| `vsock-inetd`    | Accepts vsock connections and runs a program per client with its stdin/stdout connected to the client. |
| `vsock-netcat`   | netcat for vsock: connect or listen, relay stdin/stdout or run a program (`-e`). |

The daemons accept clients from every peer unless restricted (`-allow-cid` for
vsock listeners, `-allow-ip` for the TCP listener), serve every client in its
own goroutine (inetd *nowait*), and log each accepted, rejected and finished
connection to stderr. With `-v` they also log the size of every read and
write.

## Requirements

* Windows guest with the **viosock** driver from
  [virtio-win](https://github.com/virtio-win/kvm-guest-drivers-windows)
  installed (it provides the vsock Winsock provider).
* A vsock device on the VM, e.g. `-device vhost-vsock-pci,guest-cid=3`
  or `<vsock model='virtio'><cid auto='yes'/></vsock>` in libvirt.

The vsock address family is looked up in the Winsock catalog at startup.
If detection fails, pass it with `-af` (viosock uses 40).

## viosock driver notes

Verified on Windows 11 (build 26300) with the viosock provider
"Virtio Vsock STREAM" (AF 40) and a Linux vhost-vsock host:

* **Socket handles do not support `ReadFile`/`WriteFile`.** Both fail with
  `ERROR_INVALID_PARAMETER`, even though the provider advertises
  `XP1_IFS_HANDLES`; only `WSARecv`/`WSASend` work. A child process
  therefore cannot use the socket as stdin/stdout directly, so vsock-inetd
  and `vsock-netcat -e` relay through pipes by default (`-direct` hands over
  the raw handle for providers that support it).
* **Blocking sends do not wait for peer credit.** When the peer's receive
  buffer is full (256 KiB by default on a Linux host), `WSASend` on a
  blocking socket fails at once with the raw NTSTATUS `0xC0000023`
  (`STATUS_BUFFER_TOO_SMALL`) instead of blocking. The tools retry with
  smaller chunks and then back off (up to 20 ms between attempts) until the
  peer frees space, so slow readers on the host side are handled.
* **Avoid zero-length I/O.** During testing, zero-length `WSASend` calls (in
  a probe also using `select()` and `FIONREAD`) were followed by the QEMU
  process exiting; the exact trigger was not isolated. The tools never issue
  zero-length reads or writes.

## Building

Go 1.25 or newer and GNU make:

```sh
make                 # bin/windows-amd64/*.exe and bin/windows-arm64/*.exe
make windows-amd64   # x64 only
make windows-arm64   # ARM64 only
make vet test        # static checks and unit tests
make dist            # zip archive per architecture
```

## Common options

All tools:

| Flag | Meaning |
|---|---|
| `-af N` | vsock address family; 0 (default) detects it |
| `-v` | verbose: log every read/write with its byte count (vsock-netcat: see below) |
| `-version` | print version |

vsock listeners (`vsock-tcpproxy`, `vsock-inetd`):

| Flag | Meaning |
|---|---|
| `-port N` | vsock port to listen on (required) |
| `-allow-cid LIST` | comma-separated peer CIDs allowed to connect; repeatable; default any |
| `-backlog N` | listen backlog (128) |

TCP options (`vsock-tcpproxy`, `vsock-tcplisten`), applied to the TCP side:

| Flag | Default | Meaning |
|---|---|---|
| `-nodelay` | `true` | TCP_NODELAY (`-nodelay=false` re-enables Nagle) |
| `-keepalive` | `false` | enable TCP keepalive |
| `-keepalive-idle` | `15s` | idle time before the first probe |
| `-keepalive-interval` | `15s` | interval between probes |
| `-keepalive-count` | `9` | unanswered probes before the connection is dropped |

## vsock-tcpproxy

```bat
vsock-tcpproxy -port 3389 -target 127.0.0.1:3389 -allow-cid 2 -keepalive
```

| Flag | Default | Meaning |
|---|---|---|
| `-target HOST:PORT` | | TCP destination (required) |
| `-connect-timeout` | `10s` | TCP connect timeout |

plus the TCP options above.

When one side finishes sending, the other side is half-closed, so
request/response protocols relying on shutdown work through the proxy.

From the host: `socat TCP-LISTEN:13389,fork VSOCK-CONNECT:3:3389`.

## vsock-tcplisten

The reverse direction: a TCP service in the guest that reaches a vsock
service on the host (or another VM).

```bat
vsock-tcplisten -listen 127.0.0.1:2222 -cid 2 -port 22 -keepalive
```

| Flag | Default | Meaning |
|---|---|---|
| `-listen ADDR:PORT` | | TCP address to listen on, e.g. `127.0.0.1:2222` or `:2222` (required) |
| `-cid N` | `2` | vsock CID to connect to (2 is the host) |
| `-port N` | | vsock port to connect to (required) |
| `-allow-ip LIST` | any | comma-separated client addresses or CIDR prefixes allowed to connect; repeatable |

plus the TCP options above (keepalive is applied to accepted connections).
Listening on all interfaces exposes the vsock service to the network; prefer
a loopback address or restrict clients with `-allow-ip`.

On the host, e.g.: `socat VSOCK-LISTEN:22,fork TCP:127.0.0.1:22`.

## vsock-inetd

```bat
vsock-inetd -port 5000 -allow-cid 2 -- C:\Windows\System32\cmd.exe /q
vsock-inetd -port 5001 -stderr socket -v -- powershell.exe -NoLogo -NoProfile -Command -
```

Everything after the flags (or after `--`) is the program and its
arguments. The program is resolved through `PATH` once at startup.

| Flag | Default | Meaning |
|---|---|---|
| `-wait` | `false` | inetd *wait* mode: serve one client at a time |
| `-direct` | `false` | hand the socket handle itself to the child instead of relayed pipes (not supported by viosock, see below) |
| `-stderr MODE` | `inherit` | child stderr: `inherit` (vsock-inetd's stderr), `socket` or `null` |

The child environment additionally contains `VSOCK_REMOTE_CID`,
`VSOCK_REMOTE_PORT`, `VSOCK_LOCAL_CID` and `VSOCK_LOCAL_PORT`.

**Pipe mode (default).** The child's stdin/stdout are anonymous pipes and
vsock-inetd copies between them and the socket, so `-v` shows every
transfer. Pipes are inherited through `PROC_THREAD_ATTRIBUTE_HANDLE_LIST`,
so concurrently started children never see each other's handles.

**Handle mode (`-direct`).** The accepted SOCKET itself is made the child's
`hStdInput`/`hStdOutput` through `STARTUPINFO`, like classic inetd, and the
data does not pass through vsock-inetd. This requires a Winsock provider
whose socket handles work with `ReadFile`/`WriteFile`. **The virtio-win
viosock provider's do not**: both calls fail with `ERROR_INVALID_PARAMETER`
(verified on Windows 11 build 26300), and a child such as `cmd.exe` exits
at once reporting "The process tried to write to a nonexistent pipe". The
mode is kept for providers that support it.

From the host: `socat - VSOCK-CONNECT:3:5000`.

## vsock-netcat

```bat
vsock-netcat [options] CID PORT        :: connect: stdin -> vsock, vsock -> stdout
vsock-netcat -l [options] PORT         :: listen, serve one client, exit
vsock-netcat -l -k [options] PORT      :: keep listening, clients one after another
```

Examples:

```bat
vsock-netcat 2 5000 < request.bin > response.bin
vsock-netcat -z -w 2s 2 5000 && echo host is listening
vsock-netcat -l -k -e cmd.exe 5000
```

| Flag | Default | Meaning |
|---|---|---|
| `-l` | | listen instead of connecting |
| `-k` | | with `-l`: keep listening; clients are served one at a time |
| `-allow-cid LIST` | any | with `-l`: allowed peer CIDs |
| `-w DURATION` | none | connect timeout; with `-l`, how long to wait for the first client |
| `-q DURATION` | wait | after EOF on stdin, close the connection after this delay instead of waiting for the peer |
| `-z` | | connect and close at once; the exit status tells whether the port is open |
| `-e PROGRAM` | | run PROGRAM with stdin/stdout connected to the peer |
| `-direct`, `-stderr` | | as for vsock-inetd, with `-e` |
| `-v` | | report connections |
| `-vv` | | also log every read/write with its byte count |

Behaviour:

* **Half-close by default.** EOF on stdin shuts down the sending side of the
  connection; vsock-netcat keeps printing what the peer sends and exits when
  the peer closes. Use `-q 0` (or a delay) for peers that never close.
* **Quiet by default.** Only warnings and errors go to stderr; `-v` and
  `-vv` add connection and transfer details. Flags may follow the
  positional arguments (`vsock-netcat 2 5000 -v`).
* **Binary-safe.** stdin and stdout are copied byte for byte. From an
  interactive console input is line-buffered by Windows; Ctrl+Z, Enter sends
  EOF and Ctrl+C exits.
* **Exit status:** 0 on success; with `-e`, the child's exit code;
  1 when connecting, listening or relaying fails or no client arrives
  within `-w`; 2 for usage errors.

## Log format

```
2026/10/30 20:23:01.123456 vsock-tcpproxy[4120]: INFO  dev listening on vsock any:3389 (af 40), allowed peer CIDs: 2, mode: nowait (one goroutine per client)
2026/10/30 20:23:05.002113 vsock-tcpproxy[4120]: INFO  [#1 cid=2 port=1035] accepted connection from 2:1035 on 3:3389 (active: 1)
2026/10/30 20:23:05.004010 vsock-tcpproxy[4120]: INFO  [#1 cid=2 port=1035] connected to tcp 127.0.0.1:3389 from 127.0.0.1:50122, relaying
2026/10/30 20:23:05.010231 vsock-tcpproxy[4120]: DEBUG [#1 cid=2 port=1035] vsock->tcp: read 512 bytes
2026/10/30 20:23:05.010260 vsock-tcpproxy[4120]: DEBUG [#1 cid=2 port=1035] vsock->tcp: wrote 512 bytes (total 512)
2026/10/30 20:24:11.551002 vsock-tcpproxy[4120]: INFO  [#1 cid=2 port=1035] closed: vsock->tcp 81234 bytes, tcp->vsock 1203321 bytes
2026/10/30 20:24:11.551040 vsock-tcpproxy[4120]: INFO  [#1 cid=2 port=1035] connection finished after 1m6.549s (active: 0)
2026/10/30 20:24:20.100771 vsock-tcpproxy[4120]: WARN  [#2 cid=5 port=1036] rejected connection from 5:1036: CID 5 not allowed
```

## License

BSD 3-Clause, see [LICENSE](LICENSE).
