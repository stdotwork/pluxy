// Command frametap is a test extension that runs the extension program named inner beside it and
// passes every connection from core through to it, writing one line per frame to frames.log
// beside it: "<n> core <op or type> capture=<true|false>" for each frame core sent and
// "<n> ext <type>" for each frame the extension sent, n numbering the connections. Core sees
// inner's handshake and replies unchanged, so a test can count what crosses the socket.
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "frametap:", err)
		os.Exit(1)
	}
}

func run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	socket := os.Getenv("PLUXY_SOCKET")
	if socket == "" {
		return fmt.Errorf("PLUXY_SOCKET is not set")
	}
	innerSocket := socket + ".in"
	log, err := os.OpenFile(filepath.Join(dir, "frames.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	record := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = log.WriteString(line + "\n")
	}

	cmd := exec.Command(filepath.Join(dir, "inner"))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PLUXY_SOCKET=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PLUXY_SOCKET="+innerSocket)
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr // core closing stdin stops inner too
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r := bufio.NewReader(out)
	line, err := r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("inner sent no handshake: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(line), "|")
	if len(parts) != 4 || parts[3] != innerSocket {
		return fmt.Errorf("inner's handshake %q", line)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	fmt.Printf("%s|%s|unix|%s\n", parts[0], parts[1], socket)
	go func() { _, _ = io.Copy(os.Stdout, r) }()
	go func() {
		_ = cmd.Wait()
		os.Exit(0)
	}()
	for n := 1; ; n++ {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go relay(c, innerSocket, func(line string) { record(fmt.Sprintf("%d %s", n, line)) })
	}
}

// relay passes frames between core's connection c and a new connection to inner, recording each.
func relay(c net.Conn, innerSocket string, record func(string)) {
	defer c.Close()
	in, err := net.Dial("unix", innerSocket)
	if err != nil {
		return
	}
	defer in.Close()
	done := make(chan struct{}, 2)
	pass := func(from, to net.Conn, name func(map[string]any) string) {
		defer func() { done <- struct{}{} }()
		for {
			var head [4]byte
			if _, err := io.ReadFull(from, head[:]); err != nil {
				return
			}
			body := make([]byte, binary.BigEndian.Uint32(head[:]))
			if _, err := io.ReadFull(from, body); err != nil {
				return
			}
			var f map[string]any
			_ = json.Unmarshal(body, &f)
			record(name(f))
			if _, err := to.Write(append(head[:], body...)); err != nil {
				return
			}
		}
	}
	go pass(c, in, func(f map[string]any) string {
		what, _ := f["op"].(string)
		if what == "" {
			what, _ = f["type"].(string)
		}
		capture, _ := f["capture"].(bool)
		return fmt.Sprintf("core %s capture=%t", what, capture)
	})
	go pass(in, c, func(f map[string]any) string {
		t, _ := f["type"].(string)
		return "ext " + t
	})
	<-done
}
