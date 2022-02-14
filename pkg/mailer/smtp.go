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

type SMTPMailer struct {
	auth smtp.Auth
	host string
	addr string
	user string
}

func (s *SMTPMailer) sendMailTLS(from string, to []string, body []byte) error {
	conn, err := tls.Dial("tcp", s.addr, &tls.Config{ServerName: s.host})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, s.host)
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
	msg := fmt.Sprintf("MIME-version: 1.0;\nContent-Type: text/plain; charset=\"UTF-8\";\r\n")
	msg += fmt.Sprintf("From: %s\r\n", from)
	msg += fmt.Sprintf("To: %s\r\n", strings.Join(to, ";"))
	msg += fmt.Sprintf("Subject: %s\r\n", subject)
	msg += fmt.Sprintf("\r\n%s\r\n", content)
	return s.sendMailTLS(s.user, to, []byte(msg))
}

func NewSMTPMailer(addr, username, password string) Mailer {
	host, _, _ := net.SplitHostPort(addr)
	s := &SMTPMailer{auth: smtp.PlainAuth("", username, password, host), host: host, addr: addr}
	s.user = username
	return s
}
