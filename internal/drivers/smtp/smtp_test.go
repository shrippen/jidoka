package smtp

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"jidoka/internal/model"
)

// fakeServer is a minimal SMTP server: no STARTTLS, AUTH PLAIN, one mail.
type fakeServer struct {
	addr string
	got  chan session
}

type session struct {
	auth string
	from string
	to   []string
	data string
}

func startFake(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	f := &fakeServer{addr: ln.Addr().String(), got: make(chan session, 1)}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		f.serve(conn)
	}()
	return f
}

func (f *fakeServer) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	say := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
	var s session

	say("220 fake")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(up, "EHLO"):
			say("250-fake")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(up, "AUTH PLAIN"):
			s.auth = strings.TrimSpace(line[len("AUTH PLAIN"):])
			say("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			s.from = line[len("MAIL FROM:"):]
			say("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			s.to = append(s.to, line[len("RCPT TO:"):])
			say("250 ok")
		case up == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.data = b.String()
			say("250 ok")
		case up == "QUIT":
			say("221 bye")
			f.got <- s
			return
		default:
			say("250 ok")
		}
	}
}

func TestSendPlainWithAuth(t *testing.T) {
	f := startFake(t)
	cfg := model.Mail{SMTP: f.addr, User: "jidoka", From: "Jidoka <j@example.org>"}

	err := Send(context.Background(), cfg, "pw", Mail{
		To: []string{"a@example.org", "B <b@example.org>"}, Subject: "Hallo Ü", Body: "Zeile 1\nZeile 2",
	})
	if err != nil {
		t.Fatal(err)
	}

	s := <-f.got
	raw, _ := base64.StdEncoding.DecodeString(s.auth)
	if string(raw) != "\x00jidoka\x00pw" {
		t.Errorf("auth = %q", raw)
	}
	if s.from != "<j@example.org>" || len(s.to) != 2 || s.to[1] != "<b@example.org>" {
		t.Errorf("envelope from=%q to=%q", s.from, s.to)
	}
	if !strings.Contains(s.data, "Subject: =?utf-8?q?Hallo_=C3=9C?=") {
		t.Errorf("subject not encoded:\n%s", s.data)
	}
}

func TestSendNoAuthWithoutUser(t *testing.T) {
	f := startFake(t)
	err := Send(context.Background(), model.Mail{SMTP: f.addr, From: "j@example.org"}, "",
		Mail{To: []string{"a@example.org"}, Subject: "s", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if s := <-f.got; s.auth != "" {
		t.Errorf("auth sent: %q", s.auth)
	}
}

func TestSendNeedsRecipient(t *testing.T) {
	if err := Send(context.Background(), model.Mail{}, "", Mail{}); err == nil {
		t.Fatal("want error")
	}
}

func TestBuildAttachment(t *testing.T) {
	now := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	raw, err := build("j@example.org", Mail{
		To: []string{"a@example.org"}, Subject: "x\r\nBcc: evil@example.org", Body: "kurz",
		Attach: &Attachment{Name: "out.txt", Text: "ganze Ausgabe ü"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	msg, err := netmail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Bcc") != "" {
		t.Fatal("header injection")
	}

	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" {
		t.Fatalf("type %q err %v", mt, err)
	}

	mr := multipart.NewReader(msg.Body, params["boundary"])
	var texts []string
	var names []string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(quotedprintable.NewReader(p))
		texts = append(texts, string(b))
		names = append(names, p.FileName())
	}
	if len(texts) != 2 || texts[0] != "kurz" || texts[1] != "ganze Ausgabe ü" || names[1] != "out.txt" {
		t.Fatalf("parts %q names %q", texts, names)
	}
}

func TestBuildPlain(t *testing.T) {
	raw, err := build("j@example.org", Mail{To: []string{"a@example.org"}, Subject: "s", Body: "b"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "multipart") {
		t.Fatal("plain mail is multipart")
	}
}
