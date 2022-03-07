package mailer

import (
	"context"
	"time"

	"github.com/mailgun/mailgun-go/v4"
	"go.uber.org/zap"
)

type MailgunMailer struct {
	mg mailgun.Mailgun
}

func (m *MailgunMailer) SendMail(from string, to []string, subject, content string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg := m.mg.NewMessage(from, subject, content, to...)
	resp, id, err := m.mg.Send(ctx, msg)
	zap.L().Debug("Mailgun.Send", zap.String("resp", resp), zap.String("id", id), zap.Error(err))
	return err
}

func NewMailgunMailer(domain string, apiKey string) *MailgunMailer {
	mg := mailgun.NewMailgun(domain, apiKey)
	return &MailgunMailer{mg: mg}
}
