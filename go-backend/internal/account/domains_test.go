package account

import (
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// writeDomain stores a domain config through the provider, which encodes the
// file name the way the PHP backend does ("*" becomes "_wildcard_").
func writeDomain(t *testing.T, provider *domain.Provider, config *domain.Domain) {
	t.Helper()

	if err := provider.Save(config); nil != err {
		t.Fatalf("save domain: %v", err)
	}
}

func TestDomainsGetByEmailAddress(t *testing.T) {
	dir := t.TempDir()

	provider := domain.NewProvider(dir, nil, domain.Defaults{})

	config := testDomain("example.com")
	config.WhiteList = "allowed@example.com"
	writeDomain(t, provider, config)

	// mail.example.net is served by the wildcard config.
	wildcard := testDomain("example.net")
	wildcard.Name = "*.example.net"
	writeDomain(t, provider, wildcard)

	hooks := &hookRecorder{}
	domains := NewDomains(provider)
	domains.Plugins = hooks

	resolved, err := domains.GetByEmailAddress("allowed@example.com")
	if nil != err {
		t.Fatalf("GetByEmailAddress: %v", err)
	}
	if nil == resolved || "example.com" != resolved.Name {
		t.Fatalf("resolved = %v", resolved)
	}
	if !hooks.called("filter.domain") {
		t.Error("the filter.domain hook did not run")
	}

	// A whitelisted domain refuses another address.
	if _, err := domains.GetByEmailAddress("other@example.com"); nil == err {
		t.Error("a non whitelisted address must fail")
	} else if code := clientCode(t, err); appctx.AccountNotAllowed != code {
		t.Errorf("code = %d, want AccountNotAllowed", code)
	}

	// The wildcard config is found through WithWildcard().
	if resolved, err := domains.GetByEmailAddress("user@mail.example.net"); nil != err || nil == resolved {
		t.Errorf("wildcard lookup = %v, %v", resolved, err)
	}

	// An unknown domain reports DomainNotAllowed.
	if _, err := domains.GetByEmailAddress("user@unknown.tld"); nil == err {
		t.Error("an unknown domain must fail")
	} else if code := clientCode(t, err); appctx.DomainNotAllowed != code {
		t.Errorf("code = %d, want DomainNotAllowed", code)
	}
}

func TestDomainsWithoutProvider(t *testing.T) {
	domains := NewDomains(nil)

	if nil != domains.Load("example.com") {
		t.Error("Load without a provider must return nil")
	}

	_, err := domains.GetByEmailAddress("user@example.com")
	if nil == err {
		t.Fatal("GetByEmailAddress without a provider must fail")
	}
	if code := clientCode(t, err); appctx.DomainNotAllowed != code {
		t.Errorf("code = %d, want DomainNotAllowed", code)
	}
}

func TestEmailDomainAndLocalPart(t *testing.T) {
	tests := []struct {
		email     string
		wantLocal string
		wantHost  string
	}{
		{"user@example.com", "user", "example.com"},
		{"user.name+tag@mail.example.com", "user.name+tag", "mail.example.com"},
		{"user@sub@example.com", "user@sub", "example.com"},
		{"no-at-sign", "no-at-sign", ""},
	}

	for _, test := range tests {
		if got := EmailLocalPart(test.email); test.wantLocal != got {
			t.Errorf("EmailLocalPart(%q) = %q, want %q", test.email, got, test.wantLocal)
		}
		if got := EmailDomain(test.email); test.wantHost != got {
			t.Errorf("EmailDomain(%q) = %q, want %q", test.email, got, test.wantHost)
		}
	}
}
