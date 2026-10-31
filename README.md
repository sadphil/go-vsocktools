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

## Why virtio sockets

A virtual socket (vsock, `AF_VSOCK`) is a stream channel between a virtual
machine and its hypervisor host, carried over a virtio device instead of a
network. Endpoints are addressed by a *context ID* (CID) and a port: the
host is always CID 2, and each guest gets its own CID from the VM
configuration. Using it instead of TCP/IP between host and guest has
practical benefits:

* **No network needed.** There are no IP addresses, subnets, DHCP, routes,
  bridges, NAT or port forwarding to set up. The channel works when the
  guest has no network adapter at all, when its network is down or
  misconfigured, or when the VM is deliberately isolated from every
  network.
* **Not reachable from the network.** A vsock service can only be reached
  by the host (or, where the hypervisor allows it, by sibling VMs), never
  by other machines on the LAN or the internet. It does not depend on
  firewall rules to stay private, and a network misconfiguration cannot
  expose it.
* **The hypervisor vouches for the peer.** The host assigns CIDs, and a
  guest cannot claim another guest's CID. A service can therefore decide
  which machine may connect by CID (`-allow-cid 2` accepts only the
  host). The CID identifies the machine, not the user or process on it:
  any process on the host can connect from CID 2.
* **Stable, simple addressing.** A guest's CID stays the same no matter how
  its network is renumbered or reconfigured, so host-side tooling always
  knows where to connect.
* **Less overhead and fewer moving parts.** Data goes through the virtio
  transport directly, without IP routing, TCP congestion control, checksums
  or the guest's network stack and firewall in the path.

Typical uses are management and automation from the host (remote shells,
agents, configuration and provisioning), reaching RDP or SSH in a guest
that has no network, collecting logs or metrics, and moving files into or
out of isolated VMs.

These tools make that channel usable without changing existing software:
`vsock-tcpproxy` and `vsock-tcplisten` connect vsock to ordinary TCP
programs in either direction, `vsock-inetd` serves any console program over
vsock, and `vsock-netcat` covers scripting and ad-hoc transfers.

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
* **`select()` is broken and can bugcheck Windows.** Calling `select()`
  with **more than one non-empty fd set** (any two of read/write/exception,
  with the same or different sockets) reproducibly crashes the guest with
  stop code `0x10D WDF_VIOLATION`, parameter 1 `0x4` (NULL handle passed to
  a framework method), caller `viosock.sys+0x182f1` (viosock driver
  100.103.104.30200, virtio-win mm322). With a single set, a write set
  works (also with several entries), while a read-only or exception-only
  `select()` that has nothing ready fails with `WSAEINVAL` (not yet
  explained).

  *Root cause* (virtio-win `master` as of `bd196cda`, 2026-10-23, still
  affected): all three fd sets share one handle array, and
  `VIOSockSelectGetFdsWriteSet()`/`GetFdsExcptSet()` derive each set's start
  from `FdCount[]` of the preceding sets. `VIOSockSelectCleanupPkt()`
  (`viosock/sys/Device.c`) evaluates those macros only after the previous
  `VIOSockSelectCleanupFds()` call has zeroed that set's count, so the next
  set points back at entries that were just cleared to NULL and
  `GetSocketContext(NULL)` bugchecks. The offset error dates back to
  `cea94bb0` (2020), where it silently released the read set's socket
  references a second time; `90935e49` (2026-04, mm321+) started clearing
  the entries, which turned it into a deterministic bugcheck. The fix is to
  compute all three set pointers before cleaning up any of them; a patch
  exists (not yet built or submitted upstream).
  With `-cpu ...,hv-passthrough` (which includes `hv-crash`) Windows
  reports the bugcheck through the Hyper-V crash MSRs and QEMU's default
  `-action panic=shutdown` ends the VM at once (QEMU 10.2.3), so neither a
  dump nor a bugcheck code is recorded and the VM simply vanishes. Run QEMU
  with `-d guest_errors -D qemu.log` to see the "HV crash parameters", or
  with `-action panic=none` to let Windows write a dump.
  `ioctlsocket(FIONREAD)` is harmless. The tools never call
  `select()`/`WSAPoll`; all I/O is plain blocking `WSARecv`/`WSASend`. Do
  not wrap these sockets in code that polls them.

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

## Test results

Tested 2026-10-30 with the windows/amd64 builds of the code as of
`d6affe1` on a Windows 11 guest (build 26300, viosock driver
100.103.104.30200, guest CID 9500) under QEMU 10.2.3 / vhost-vsock-pci on
Linux 6.18, with `socat` as the host-side peer. The fixes in `d6affe1`
(pipes by default, credit back-off, netcat stdin after peer half-close)
came out of this testing.

| Tool | Test | Result |
|---|---|---|
| all | Winsock provider detection ("Virtio Vsock STREAM", AF 40), peer CID logging | pass |
| vsock-netcat | listen and connect, data in both directions, half-close | pass |
| vsock-netcat | `-z` against a refused port and an unknown CID; `-l -w 2s` timeout | pass, exit status 1 |
| vsock-netcat | `-l -e cmd.exe`, child exit code becomes netcat's exit status | pass |
| vsock-netcat | `-l -k`, three clients in a row | pass |
| vsock-netcat | `-q 1s` with a peer that stays open | pass, closed 1.0 s after stdin EOF |
| vsock-netcat | 32 MiB to a host reader that pauses 3 s (peer credit exhausted) | pass, SHA-256 matches |
| vsock-inetd | `cmd.exe` for two concurrent clients, `VSOCK_*` environment, exit codes | pass |
| vsock-inetd | `-stderr socket`, `-v` byte counts, `-allow-cid` rejection | pass |
| vsock-inetd | `-wait`: second client accepted only when the first finished | pass |
| vsock-inetd | program removed after startup | pass, error logged, client closed, keeps serving |
| vsock-tcpproxy | SSH session to the guest's sshd through the proxy; 32 MiB scp | pass, SHA-256 matches |
| vsock-tcpproxy | TCP target down | pass, error logged, client closed, keeps serving |
| vsock-tcplisten | 32 MiB echo round trips through an `ssh -L` tunnel | pass, SHA-256 matches |
| vsock-tcplisten | `-allow-ip` rejection; vsock target refused; unknown CID | pass, error logged, keeps serving |
| tcpproxy + tcplisten | 20 parallel 1 MiB echo round trips, host → tcpproxy → TCP → tcplisten → host | pass, 20/20 in 1.8 s, no warnings |

Driver probes (see [viosock driver notes](#viosock-driver-notes)):
`ReadFile`/`WriteFile` on socket handles fail; `ioctlsocket(FIONREAD)` and
zero-length `WSASend` (on an open connection and after the peer
half-closed) are harmless; `select()` with more than one fd set bugchecks
the guest.

Not tested: Ctrl+C handling (sshd cannot deliver a console Ctrl+C),
`vsock-netcat` with an interactive console as stdin, `-direct` with a
provider that supports it, and the windows/arm64 builds.

## License

BSD 3-Clause, see [LICENSE](LICENSE).
