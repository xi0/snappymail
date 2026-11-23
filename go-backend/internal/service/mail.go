package service

// The per-request mail layer of the Do* actions: Actions::MailClient() and
// initMailClientConnection() (internal/handler.Mail).
//
// It reuses the request session the AppData endpoints already build, so the
// account resolution, the token checks and the domain configuration are the
// same ones the rest of the backend uses.

import (
	"errors"
	"net/http"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/mail"
)

// Mail is handler.Mail: the IMAP connection of one request.
//
// A request may run several Do* actions (the frontend batches some), so the
// connection is opened once and handed out again, like Actions::$oMailClient.
type Mail struct {
	// Session is the request session (cookies + account).
	Session *Session
	// Config is application.ini, mixed into the domain IMAP settings by
	// Account::ImapConnectAndLogin().
	Config imapclient.Config
	// Plugins runs the imap.* connect/login hooks (step 28).
	Plugins account.Hooks
	// Logger receives the IMAP log lines.
	Logger *logx.Logger

	connected bool
	account   *account.Account
	client    *mail.Client
}

// NewMail builds the mail layer of one request.
func NewMail(
	w http.ResponseWriter,
	r *http.Request,
	newSession func(http.ResponseWriter, *http.Request) *Session,
	config imapclient.Config,
	logger *logx.Logger,
) *Mail {
	session := (*Session)(nil)
	if nil != newSession {
		session = newSession(w, r)
	}

	return &Mail{Session: session, Config: config, Logger: logger}
}

// Connect is Actions::initMailClientConnection() + Actions::MailClient().
func (m *Mail) Connect() (*account.Account, *mail.Client, error) {
	if m.connected {
		return m.account, m.client, nil
	}
	m.connected = true

	if nil == m.Session || nil == m.Session.Authenticator {
		return nil, nil, appctx.NewClientError(appctx.InvalidToken, nil, "Account undefined")
	}

	// getAccountFromToken() throws InvalidToken when no account is logged in.
	resolved, err := m.Session.Authenticator.GetAccountFromToken(true)
	if nil != err {
		return nil, nil, err
	}
	if nil == resolved {
		return nil, nil, appctx.NewClientError(appctx.InvalidToken, nil, "Account undefined")
	}

	client, err := imapclient.ConnectAndLogin(resolved, m.Config, m.Plugins, m.Logger)
	if nil != err {
		// imapConnect() maps MailSo\Net\Exceptions\ConnectionException onto
		// ConnectionError and everything else onto AuthError.
		var mailErr *auth.MailError
		if errors.As(err, &mailErr) && auth.MailErrorConnection == mailErr.Kind {
			return nil, nil, appctx.NewClientError(appctx.ConnectionError, err, "")
		}
		return nil, nil, appctx.NewClientError(appctx.AuthError, err, "")
	}

	m.account = resolved
	m.client = mail.New(client)

	return m.account, m.client, nil
}
