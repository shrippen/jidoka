// Package smtp sends one mail (M1, M8).
//
//	Send ──► dial ──► STARTTLS (if offered) ──► AUTH PLAIN (if user set) ──► DATA
//
// The message is multipart/mixed when it has an attachment (attach_output).
package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"jidoka/internal/model"
)

const sendTimeout = 30 * time.Second

// Attachment is a text file, e.g. the whole output of a run.
type Attachment struct {
	Name string
	Text string
}

// Mail is one message. To is required; From and server come from the config.
type Mail struct {
	To      []string
	Subject string
	Body    string
	Attach  *Attachment
}

// Send delivers m through cfg.SMTP ("host:port"). Auth only when cfg.User is
// set; PLAIN is refused by net/smtp on an unencrypted link to a remote host,
// so the password never travels in clear text.
func Send(ctx context.Context, cfg model.Mail, password string, m Mail) error {
	if len(m.To) == 0 {
		return errors.New("smtp: no recipient")
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	msg, err := build(cfg.From, m, time.Now())
	if err != nil {
		return err
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", cfg.SMTP)
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	host, _, _ := net.SplitHostPort(cfg.SMTP)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	defer c.Close()

	return deliver(c, host, cfg, password, m.To, msg)
}

func deliver(c *smtp.Client, host string, cfg model.Mail, password string, to []string, msg []byte) error {
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
	}

	if cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.User, password, host)); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}

	from, err := bareAddr(cfg.From)
	if err != nil {
		return err
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}

	for _, r := range to {
		addr, err := bareAddr(r)
		if err != nil {
			return err
		}
		if err := c.Rcpt(addr); err != nil {
			return fmt.Errorf("smtp: rcpt: %w", err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	return c.Quit()
}

// bareAddr turns "Jidoka <j@x.de>" into "j@x.de" for the envelope.
func bareAddr(s string) (string, error) {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return "", fmt.Errorf("smtp: bad address %q", s)
	}
	return a.Address, nil
}

// build renders the RFC 5322 message. Header values lose CR/LF so a job
// name cannot inject headers.
func build(from string, m Mail, now time.Time) ([]byte, error) {
	var buf bytes.Buffer

	hdr := func(k, v string) {
		buf.WriteString(k + ": " + oneLine(v) + "\r\n")
	}
	hdr("From", from)
	hdr("To", strings.Join(m.To, ", "))
	hdr("Subject", mime.QEncoding.Encode("utf-8", oneLine(m.Subject)))
	hdr("Date", now.Format(time.RFC1123Z))
	hdr("MIME-Version", "1.0")

	if m.Attach == nil {
		hdr("Content-Type", `text/plain; charset="utf-8"`)
		hdr("Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		if err := writeQP(&buf, m.Body); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	mw := multipart.NewWriter(&buf)
	hdr("Content-Type", `multipart/mixed; boundary="`+mw.Boundary()+`"`)
	buf.WriteString("\r\n")

	body, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/plain; charset="utf-8"`},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, err
	}
	if err := writeQP(body, m.Body); err != nil {
		return nil, err
	}

	att, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/plain; charset="utf-8"`},
		"Content-Transfer-Encoding": {"quoted-printable"},
		"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": oneLine(m.Attach.Name)})},
	})
	if err != nil {
		return nil, err
	}
	if err := writeQP(att, m.Attach.Text); err != nil {
		return nil, err
	}

	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeQP(w interface{ Write([]byte) (int, error) }, s string) error {
	q := quotedprintable.NewWriter(w)
	if _, err := q.Write([]byte(s)); err != nil {
		return err
	}
	return q.Close()
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
