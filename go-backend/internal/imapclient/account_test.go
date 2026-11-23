package imapclient

// Tests of ConnectAndLogin() (Account::ImapConnectAndLogin) and its end-to-end
// effect through the auth login flow: a wrong password must surface as the
// AuthError notification code (102), exactly like the PHP backend.

import (
	"errors"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// fakeDomains is an account.DomainProvider backed by one domain config.
type fakeDomains struct{ config *domain.Domain }

func (f fakeDomains) Load(name string, _ ...domain.LoadOption) *domain.Domain {
	if nil != f.config && f.config.Name == name {
		return f.config
	}
	return nil
}

func (f fakeDomains) GetByEmailAddress(string) (*domain.Domain, error) {
	return f.config, nil
}

// testAccount builds an account whose domain points at the mock server.
func testAccount(server *mockIMAP) *account.Account {
	target := account.NewAccount()
	target.SetCredentials(server.domainConfig(), "user@example.com",
		"user@example.com", "secret", "", "")
	return target
}

func TestConnectAndLogin(t *testing.T) {
	server := newMockIMAP(t)
	target := testAccount(server)

	client, err := ConnectAndLogin(target, nil, nil, nil)
	if nil != err {
		t.Fatalf("ConnectAndLogin: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if !client.IsLoggedIn() {
		t.Error("the client should be logged in")
	}
	if !client.HasCapability("IMAP4REV1") {
		t.Errorf("unexpected capabilities: %v", client.Capabilities())
	}
}

func TestConnectAndLoginHooks(t *testing.T) {
	server := newMockIMAP(t)
	target := testAccount(server)

	hooks := &hookRecorder{}
	if _, err := ConnectAndLogin(target, nil, hooks, nil); nil != err {
		t.Fatalf("ConnectAndLogin: %v", err)
	}

	for _, name := range []string{
		"imap.before-connect", "imap.after-connect", "imap.before-login", "imap.after-login",
	} {
		if !hooks.has(name) {
			t.Errorf("hook %q did not run: %v", name, hooks.names)
		}
	}

	// after-login receives the boolean result (PHP: RunHook(…, $bResult, …)).
	args := hooks.argsFor("imap.after-login")
	if 4 <= len(args) {
		if result, ok := args[2].(bool); !ok || !result {
			t.Errorf("after-login result = %v", args[2])
		}
	} else {
		t.Errorf("after-login args = %v", args)
	}
}

func TestConnectAndLoginWrongPasswordMapsToAuthError(t *testing.T) {
	server := newMockIMAP(t)

	config := server.domainConfig()
	login := &auth.Login{
		Context: &account.Context{Domains: fakeDomains{config: config}},
		IMAPLogin: func(target *account.Account) error {
			_, err := ConnectAndLogin(target, nil, nil, nil)
			return err
		},
	}

	_, err := login.LoginProcess("user@example.com", "wrong")
	if nil == err {
		t.Fatal("a wrong password must fail")
	}

	var clientError *appctx.ClientError
	if !errors.As(err, &clientError) {
		t.Fatalf("error is not a ClientError: %v", err)
	}
	if appctx.AuthError != clientError.Code {
		t.Errorf("code = %d, want AuthError 102", clientError.Code)
	}
}

func TestConnectAndLoginValidPasswordThroughAuth(t *testing.T) {
	server := newMockIMAP(t)

	config := server.domainConfig()
	login := &auth.Login{
		Context: &account.Context{Domains: fakeDomains{config: config}},
		IMAPLogin: func(target *account.Account) error {
			_, err := ConnectAndLogin(target, nil, nil, nil)
			return err
		},
	}

	main, err := login.LoginProcess("user@example.com", "secret")
	if nil != err {
		t.Fatalf("LoginProcess: %v", err)
	}
	if nil == main || "user@example.com" != main.Email() {
		t.Fatalf("main = %v", main)
	}
}

// hookRecorder records the hooks that ran and their arguments.
type hookRecorder struct {
	names []string
	args  [][]any
}

func (h *hookRecorder) RunHook(name string, args ...any) {
	h.names = append(h.names, name)
	h.args = append(h.args, args)
}

func (h *hookRecorder) has(name string) bool {
	for _, recorded := range h.names {
		if recorded == name {
			return true
		}
	}
	return false
}

func (h *hookRecorder) argsFor(name string) []any {
	for index, recorded := range h.names {
		if recorded == name {
			return h.args[index]
		}
	}
	return nil
}
