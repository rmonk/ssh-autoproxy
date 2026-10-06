package proxy

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
)

// SOCKS5 protocol constants (RFC 1928). Only no-auth CONNECT is
// supported, which is all browsers and ssh -D need.
const (
	socksVersion = 0x05

	methodNoAuth       = 0x00
	methodNoAcceptable = 0xff

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSucceeded           = 0x00
	repGeneralFailure      = 0x01
	repHostUnreachable     = 0x04
	repConnectionRefused   = 0x05
	repCommandNotSupported = 0x07
	repAddrNotSupported    = 0x08
)

// socksError is a failure that carries the SOCKS5 reply code to report,
// whether it came from an upstream ssh -D or a direct dial.
type socksError struct {
	code byte
	err  error
}

func (e *socksError) Error() string { return e.err.Error() }
func (e *socksError) Unwrap() error { return e.err }

// replyCode maps a dial error to the SOCKS5 reply code to send back.
func replyCode(err error) byte {
	var se *socksError
	if errors.As(err, &se) {
		return se.code
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return repHostUnreachable
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return repHostUnreachable
		}
		if errors.Is(err, errConnRefused) {
			return repConnectionRefused
		}
	}
	return repGeneralFailure
}

// readSocksRequest performs the server side of the SOCKS5 greeting and
// reads the CONNECT request, returning the requested host and port. On a
// protocol error it has already sent the client the appropriate reply.
func readSocksRequest(br *bufio.Reader, w io.Writer) (host string, port int, err error) {
	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return "", 0, err
	}
	if hdr[0] != socksVersion {
		return "", 0, fmt.Errorf("unsupported SOCKS version %d", hdr[0])
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return "", 0, err
	}
	noAuth := false
	for _, m := range methods {
		if m == methodNoAuth {
			noAuth = true
		}
	}
	if !noAuth {
		_, _ = w.Write([]byte{socksVersion, methodNoAcceptable})
		return "", 0, errors.New("client offered no acceptable SOCKS5 auth method")
	}
	if _, err := w.Write([]byte{socksVersion, methodNoAuth}); err != nil {
		return "", 0, err
	}

	var req [4]byte // VER CMD RSV ATYP
	if _, err := io.ReadFull(br, req[:]); err != nil {
		return "", 0, err
	}
	if req[0] != socksVersion {
		return "", 0, fmt.Errorf("unsupported SOCKS version %d in request", req[0])
	}
	host, err = readAddr(br, req[3])
	if err != nil {
		writeSocksReply(w, repAddrNotSupported)
		return "", 0, err
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(br, portBuf[:]); err != nil {
		return "", 0, err
	}
	if req[1] != cmdConnect {
		writeSocksReply(w, repCommandNotSupported)
		return "", 0, fmt.Errorf("unsupported SOCKS command %d", req[1])
	}
	return host, int(binary.BigEndian.Uint16(portBuf[:])), nil
}

// readAddr reads a SOCKS5 address of the given type.
func readAddr(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case atypIPv4, atypIPv6:
		size := 4
		if atyp == atypIPv6 {
			size = 16
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		addr, _ := netip.AddrFromSlice(buf)
		return addr.String(), nil
	case atypDomain:
		var n [1]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return "", err
		}
		buf := make([]byte, n[0])
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	default:
		return "", fmt.Errorf("unsupported SOCKS address type %d", atyp)
	}
}

// writeSocksReply sends a SOCKS5 reply with a zero bind address, which
// clients don't use for CONNECT.
func writeSocksReply(w io.Writer, code byte) {
	_, _ = w.Write([]byte{socksVersion, code, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
}

// socksConnect performs the client side of a no-auth SOCKS5 CONNECT to
// host:port over conn (an ssh -D forward). A refusal is returned as a
// *socksError carrying the upstream's reply code.
func socksConnect(conn net.Conn, host string, port int) error {
	if _, err := conn.Write([]byte{socksVersion, 1, methodNoAuth}); err != nil {
		return err
	}
	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return fmt.Errorf("upstream SOCKS5 greeting: %w", err)
	}
	if resp[0] != socksVersion || resp[1] != methodNoAuth {
		return fmt.Errorf("upstream SOCKS5 refused no-auth (method %d)", resp[1])
	}

	req := []byte{socksVersion, cmdConnect, 0x00}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Is4() {
			req = append(req, atypIPv4)
		} else {
			req = append(req, atypIPv6)
		}
		req = append(req, addr.AsSlice()...)
	} else {
		if len(host) > 255 {
			return &socksError{repAddrNotSupported, fmt.Errorf("hostname too long: %d bytes", len(host))}
		}
		req = append(req, atypDomain, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return err
	}

	var reply [4]byte // VER REP RSV ATYP
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("upstream SOCKS5 reply: %w", err)
	}
	if reply[1] != repSucceeded {
		return &socksError{reply[1], fmt.Errorf("upstream SOCKS5 CONNECT to %s failed (reply %d)",
			net.JoinHostPort(host, strconv.Itoa(port)), reply[1])}
	}
	// Discard the bound address and port.
	if _, err := readAddr(conn, reply[3]); err != nil {
		return fmt.Errorf("upstream SOCKS5 reply: %w", err)
	}
	var portBuf [2]byte
	_, err := io.ReadFull(conn, portBuf[:])
	return err
}
