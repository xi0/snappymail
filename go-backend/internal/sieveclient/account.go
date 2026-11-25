package sieveclient

import (
	"errors"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/logx"
)

// ConnectAndLogin is RainLoop\Model\Account::SieveConnectAndLogin() together
// with its private netClientLogin() helper: take the Sieve settings of the
// domain, run the sieve.* hooks, connect and authenticate.
//
// The PHP method receives the application.ini object but never reads it (unlike
// ImapConnectAndLogin), so no Config parameter is needed here.
//
// On success the caller owns the returned *Client and is expected to Disconnect()
// it. The returned error is a *Error whose Kind selects the notification code of
// Providers\Filters.
func ConnectAndLogin(target *account.Account, plugins account.Hooks, logger *logx.Logger) (*Client, error) {
	if nil == target || nil == target.Domain() {
		return nil, otherFailure(errors.New("sieveclient: account has no domain configuration"))
	}

	// The PHP method mutates the domain settings object; Go uses a per request
	// copy so two requests cannot race on the shared domain config.
	settings := target.Domain().Sieve
	client := New(&settings, logger)

	runHook(plugins, "sieve.before-connect", target, client, &settings)
	if err := client.Connect(); nil != err {
		return nil, err
	}
	runHook(plugins, "sieve.after-connect", target, client, &settings)

	runHook(plugins, "sieve.before-login", target, client, &settings)
	var loginErr error
	if settings.UseAuth {
		loginErr = client.Login(target.ImapUser(), target.ImapPass())
	}
	runHook(plugins, "sieve.after-login", target, client, nil == loginErr, &settings)

	if nil != loginErr {
		_ = client.Close()
		return nil, loginErr
	}

	return client, nil
}

// runHook runs an account/connect hook when a plugin manager is configured.
func runHook(plugins account.Hooks, name string, args ...any) {
	if nil != plugins {
		plugins.RunHook(name, args...)
	}
}
