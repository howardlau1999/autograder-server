package mailer

import (
	"context"
	"time"

	"github.com/mailgun/mailgun-go/v5"
	"go.uber.org/zap"
)

type MailgunMailer struct {
	mg     *mailgun.Client
	domain string
}

func (m *MailgunMailer) SendMail(from string, to []string, subject, content string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg := mailgun.NewMessage(m.domain, from, subject, content, to...)
	resp, err := m.mg.Send(ctx, msg)
	zap.L().Debug("Mailgun.Send", zap.String("resp", resp.Message), zap.String("id", resp.ID), zap.Error(err))
	return err
}

func NewMailgunMailer(domain string, apiKey string) *MailgunMailer {
	return &MailgunMailer{mg: mailgun.NewMailgun(apiKey), domain: domain}
}
