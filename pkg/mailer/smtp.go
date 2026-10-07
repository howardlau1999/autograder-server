package mailer

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

type Mailer interface {
	SendMail(from string, to []string, subject, content string) error
}

// SMTPTLSMode selects how the SMTP connection is secured.
type SMTPTLSMode string

const (
	// SMTPImplicitTLS dials the server over TLS from the start (e.g. port 465).
	SMTPImplicitTLS SMTPTLSMode = "implicit"
	// SMTPStartTLS connects in the clear and upgrades via STARTTLS (e.g. port 587).
	SMTPStartTLS SMTPTLSMode = "starttls"
	// SMTPNoTLS connects in the clear. smtp.PlainAuth then only sends
	// credentials to a localhost server, so this is for local relays only.
	SMTPNoTLS SMTPTLSMode = "none"
)

type SMTPMailer struct {
	auth smtp.Auth
	host string
	addr string
	user string
	mode SMTPTLSMode
}

func (s *SMTPMailer) dial() (*smtp.Client, error) {
	tlsConfig := &tls.Config{ServerName: s.host}
	if s.mode == SMTPImplicitTLS {
		conn, err := tls.Dial("tcp", s.addr, tlsConfig)
		if err != nil {
			return nil, err
		}
		return smtp.NewClient(conn, s.host)
	}
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		return nil, err
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if s.mode == SMTPStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			c.Close()
			return nil, fmt.Errorf("smtp: server %s does not advertise STARTTLS", s.addr)
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

func (s *SMTPMailer) sendMail(from string, to []string, body []byte) error {
	c, err := s.dial()
	if err != nil {
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("AUTH"); ok {
		if err = c.Auth(s.auth); err != nil {
			return err
		}
	}
	if err = c.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err = c.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}
	return c.Quit()
}

func (s *SMTPMailer) SendMail(from string, to []string, subject, content string) error {
	msg := "MIME-version: 1.0;\nContent-Type: text/plain; charset=\"UTF-8\";\r\n"
	msg += fmt.Sprintf("From: %s\r\n", from)
	msg += fmt.Sprintf("To: %s\r\n", strings.Join(to, ";"))
	msg += fmt.Sprintf("Subject: %s\r\n", subject)
	msg += fmt.Sprintf("\r\n%s\r\n", content)
	return s.sendMail(s.user, to, []byte(msg))
}

// NewSMTPMailer builds a mailer for the given address.
func NewSMTPMailer(addr, username, password string, mode SMTPTLSMode) Mailer {
	host, _, _ := net.SplitHostPort(addr)
	s := &SMTPMailer{
		auth: smtp.PlainAuth("", username, password, host),
		host: host,
		addr: addr,
		mode: mode,
	}
	s.user = username
	return s
}

// ParseSMTPTLSMode validates a configured TLS mode; "" keeps the historical
// behaviour (implicit TLS, e.g. port 465).
func ParseSMTPTLSMode(mode string) (SMTPTLSMode, error) {
	switch SMTPTLSMode(mode) {
	case "":
		return SMTPImplicitTLS, nil
	case SMTPImplicitTLS, SMTPStartTLS, SMTPNoTLS:
		return SMTPTLSMode(mode), nil
	default:
		return "", fmt.Errorf("smtp: unknown tls-mode %q (want implicit, starttls or none)", mode)
	}
}
