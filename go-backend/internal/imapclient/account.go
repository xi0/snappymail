package imapclient

import (
	"errors"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/logx"
)

// Config is the application.ini surface Account::ImapConnectAndLogin() reads.
// *config.Application satisfies it; a nil Config keeps the domain defaults.
type Config interface {
	GetString(sectionName, key, defaultValue string) string
	GetBool(sectionName, key string, defaultValue bool) bool
	GetInt(sectionName, key string, defaultValue int) int
}

// ConnectAndLogin is RainLoop\Model\Account::ImapConnectAndLogin() together with
// its private netClientLogin() helper: mix the application.ini [imap] overrides
// into the domain settings, run the connect/login hooks, connect and
// authenticate.
//
// The returned error is an *auth.MailError whose Kind selects the notification
// code of Actions\UserAuth::imapConnect() (ConnectionError 104 or AuthError 102,
// the latter with an alert for a rejected password), so it can be used directly
// as the auth.Login.IMAPLogin callback.
//
// On success the caller owns the returned *Client and is expected to reuse it
// for the IMAP commands of the same request (connection reuse per request).
func ConnectAndLogin(
	target *account.Account,
	cfg Config,
	plugins account.Hooks,
	logger *logx.Logger,
) (*Client, error) {
	if nil == target || nil == target.Domain() {
		return nil, protocolFailure(errors.New("imapclient: account has no domain configuration"))
	}

	// The PHP method mutates the domain settings object; Go uses a per request
	// copy so two requests cannot race on the shared domain config.
	settings := target.Domain().IMAP
	applyConfigOverrides(&settings, cfg)

	client := New(&settings, logger)

	runHook(plugins, "imap.before-connect", target, client, &settings)
	if err := client.Connect(); nil != err {
		return nil, err
	}
	runHook(plugins, "imap.after-connect", target, client, &settings)

	runHook(plugins, "imap.before-login", target, client, &settings)
	var loginErr error
	if settings.UseAuth {
		loginErr = client.Login(target.ImapUser(), target.ImapPass())
	}
	runHook(plugins, "imap.after-login", target, client, nil == loginErr, &settings)

	if nil != loginErr {
		_ = client.Close()
		return nil, loginErr
	}

	return client, nil
}

// applyConfigOverrides is the settings block of Account::ImapConnectAndLogin():
// the application.ini [imap] switches that widen the domain settings.
func applyConfigOverrides(settings *domain.IMAPSettings, cfg Config) {
	if nil == cfg {
		return
	}

	// $oSettings->timeout = max($oSettings->timeout, (int) $oConfig->Get('imap', 'timeout', …))
	if timeout := cfg.GetInt("imap", "timeout", settings.Timeout); timeout > settings.Timeout {
		settings.Timeout = timeout
	}

	settings.ExpungeAllOnDelete = settings.ExpungeAllOnDelete ||
		cfg.GetBool("imap", "use_expunge_all_on_delete", false)
	settings.FastSimpleSearch = !(!settings.FastSimpleSearch ||
		!cfg.GetBool("imap", "message_list_fast_simple_search", true))
	settings.FetchNewMessages = !(!settings.FetchNewMessages ||
		!cfg.GetBool("imap", "fetch_new_messages", true))
	settings.ForceSelect = settings.ForceSelect ||
		cfg.GetBool("imap", "use_force_selection", false)
	settings.MessageAllHeaders = settings.MessageAllHeaders ||
		cfg.GetBool("imap", "message_all_headers", false)

	if "" == settings.SearchFilter {
		settings.SearchFilter = strings.TrimSpace(
			cfg.GetString("imap", "message_list_permanent_filter", ""))
	}
}

// runHook runs an account/connect hook when a plugin manager is configured.
func runHook(plugins account.Hooks, name string, args ...any) {
	if nil != plugins {
		plugins.RunHook(name, args...)
	}
}
