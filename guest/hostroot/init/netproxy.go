package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func startEgressProxy() error {
	attributes := &syscall.ProcAttr{
		Env:   minimalEnvironment(),
		Files: []uintptr{0, 1, 2},
		Sys:   &syscall.SysProcAttr{Setpgid: true},
	}
	_, err := syscall.ForkExec("/usr/bin/assistant-init", []string{ //nolint:gosec // fixed helper path and arguments; environment is minimal
		"assistant-init", "netproxy", "--unix-path", nativeEgressSocket,
		"--listen", "127.0.0.1:18080",
	}, attributes)
	if err != nil {
		return fmt.Errorf("start guest egress proxy: %w", err)
	}
	return nil
}

const nativeEgressSocket = "/.assistant-egress.sock"

func runNetProxy(arguments []string) int {
	unixPath, listenAddress, err := parseNetProxyArguments(arguments)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 125
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 125
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			return 125
		}
		go serveNetProxy(connection, unixPath)
	}
}

func parseNetProxyArguments(arguments []string) (string, string, error) {
	if len(arguments) != 4 || arguments[0] != "--unix-path" || arguments[2] != "--listen" {
		return "", "", errors.New("usage: assistant-init netproxy --unix-path PATH --listen ADDRESS")
	}
	return arguments[1], arguments[3], nil
}

func serveNetProxy(client net.Conn, unixPath string) {
	defer func() { _ = client.Close() }()
	reader := bufio.NewReader(client)
	method, target, protocol, headers, err := readProxyRequest(reader)
	if err != nil {
		writeProxyError(client, "400 Bad Request")
		return
	}
	requestPath := target
	if method != "CONNECT" {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" {
			writeProxyError(client, "400 Bad Request")
			return
		}
		target = parsed.Host
		requestPath = parsed.RequestURI()
	}
	upstream, err := net.Dial("unix", unixPath) //nolint:gosec // unixPath is a fixed helper argument, not guest-controlled
	if err != nil {
		writeProxyError(client, "502 Bad Gateway")
		return
	}
	defer func() { _ = upstream.Close() }()
	if err := writeEgressTarget(upstream, target); err != nil {
		writeProxyError(client, "502 Bad Gateway")
		return
	}
	upstreamReader := bufio.NewReader(upstream)
	status, err := upstreamReader.ReadString('\n')
	if err != nil || strings.TrimSpace(status) != "OK" {
		writeProxyError(client, "502 Bad Gateway")
		return
	}
	if method == "CONNECT" {
		if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		relayProxy(client, reader, upstream, upstreamReader)
		return
	}
	if _, err := fmt.Fprintf(upstream, "%s %s %s\r\n", method, requestPath, protocol); err != nil {
		return
	}
	contentLength := int64(0)
	for _, header := range headers {
		if strings.HasPrefix(strings.ToLower(header), "proxy-connection:") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(header), "content-length:") {
			_, value, _ := strings.Cut(header, ":")
			contentLength, _ = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		}
		if _, err := io.WriteString(upstream, header+"\r\n"); err != nil {
			return
		}
	}
	if _, err := io.WriteString(upstream, "Connection: close\r\n\r\n"); err != nil {
		return
	}
	if contentLength > 0 {
		if _, err := io.CopyN(upstream, reader, contentLength); err != nil {
			return
		}
	}
	_, _ = io.Copy(client, upstreamReader)
}

func readProxyRequest(reader *bufio.Reader) (string, string, string, []string, error) {
	request, err := readProxyLine(reader)
	if err != nil {
		return "", "", "", nil, err
	}
	fields := strings.Fields(request)
	if len(fields) != 3 {
		return "", "", "", nil, errors.New("invalid HTTP request line")
	}
	var headers []string
	total := len(request)
	for {
		line, err := readProxyLine(reader)
		if err != nil {
			return "", "", "", nil, err
		}
		total += len(line)
		if total > 64<<10 {
			return "", "", "", nil, errors.New("proxy request too large")
		}
		if line == "" {
			break
		}
		headers = append(headers, line)
	}
	return fields[0], fields[1], fields[2], headers, nil
}

func readProxyLine(reader *bufio.Reader) (string, error) {
	var line strings.Builder
	for {
		part, err := reader.ReadString('\n')
		line.WriteString(part)
		if line.Len() > 64<<10 {
			return "", errors.New("proxy line too large")
		}
		if err != nil {
			return "", err
		}
		if strings.HasSuffix(part, "\n") {
			return strings.TrimRight(part, "\r\n"), nil
		}
	}
}

func writeEgressTarget(connection net.Conn, target string) error {
	if len(target) == 0 || len(target) > 4096 {
		return errors.New("invalid egress target")
	}
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(target))) //nolint:gosec // length is checked to be at most 4096
	if _, err := connection.Write(length[:]); err != nil {
		return err
	}
	_, err := io.WriteString(connection, target)
	return err
}

func relayProxy(client net.Conn, clientReader io.Reader, upstream net.Conn, upstreamReader io.Reader) {
	finished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, clientReader)
		finished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstreamReader)
		finished <- struct{}{}
	}()
	<-finished
	_ = client.Close()
	_ = upstream.Close()
	<-finished
}

func writeProxyError(client net.Conn, status string) {
	_, _ = fmt.Fprintf(client, "HTTP/1.1 %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status)
}
