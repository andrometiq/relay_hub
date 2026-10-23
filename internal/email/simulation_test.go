package email

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSMTPStalledGreetingRespectsCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	client, _ := New(Config{Enabled: true, AccountID: "simulation", Host: host, Port: n, From: "sender@example.invalid"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- client.Send(ctx, []string{"recipient@example.invalid"}, "Test", "synthetic body", nil, nil)
	}()
	select {
	case c := <-accepted:
		defer c.Close()
	case <-time.After(time.Second):
		t.Fatal("no SMTP connection")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled send reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP ignored cancellation")
	}
}

func TestSMTPRejectsHeaderInjectionBeforeConnecting(t *testing.T) {
	client, _ := New(Config{Enabled: true, AccountID: "simulation", Host: "127.0.0.1", Port: 1, From: "sender@example.invalid"})
	for _, subject := range []string{"Test\r\nBcc: hidden@example.invalid", "Test\nInjected: value"} {
		err := client.Send(t.Context(), []string{"recipient@example.invalid"}, subject, "synthetic", nil, nil)
		if err == nil || err.Error() != "invalid email header" {
			t.Fatalf("header injection not rejected: %v", err)
		}
	}
}

func TestSMTPRefusesPlaintextBeforeSendingCredentials(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan []string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			commands <- nil
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprint(conn, "220 simulation ready\r\n")
		reader := bufio.NewReader(conn)
		lines := []string{}
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				break
			}
			lines = append(lines, line)
			if strings.HasPrefix(line, "EHLO") {
				fmt.Fprint(conn, "250-simulation\r\n250 AUTH PLAIN\r\n")
			} else {
				fmt.Fprint(conn, "250 OK\r\n")
			}
		}
		commands <- lines
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	client, _ := New(Config{Enabled: true, AccountID: "simulation", Host: host, Port: n, From: "sender@example.invalid", Username: "test", Password: "synthetic-secret"})
	err = client.Send(t.Context(), []string{"recipient@example.invalid"}, "Test", "body", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "required TLS") {
		t.Fatalf("plaintext server accepted: %v", err)
	}
	for _, line := range <-commands {
		if !strings.HasPrefix(line, "EHLO") {
			t.Fatalf("sent command before TLS: %q", line)
		}
	}
}
