package account

import (
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// Domains is RainLoop\Providers\Domain: the wrapper around the domain provider
// that adds the filter.domain plugin hook and the account checks of
// getByEmailAddress().
//
// It is part of the account package because the token (de)serialization needs
// it and because the login flow of step 8 needs the same two methods.
type Domains struct {
	// Provider is the domains/ folder reader (step 4).
	Provider *domain.Provider
	// Plugins runs the filter.domain hook.
	Plugins Hooks
}

// NewDomains wraps a domain provider.
func NewDomains(provider *domain.Provider) *Domains {
	return &Domains{Provider: provider}
}

// Load is Providers\Domain::Load(): the domain config, with the filter.domain
// hook applied.
func (d *Domains) Load(name string, options ...domain.LoadOption) *domain.Domain {
	if nil == d || nil == d.Provider {
		return nil
	}

	result := d.Provider.Load(name, options...)
	if nil != result && nil != d.Plugins {
		d.Plugins.RunHook("filter.domain", result)
	}

	return result
}

// GetByEmailAddress is Providers\Domain::getByEmailAddress(): the domain config
// of an address, refusing unknown domains (DomainNotAllowed) and addresses
// outside the domain whitelist (AccountNotAllowed).
func (d *Domains) GetByEmailAddress(email string) (*domain.Domain, error) {
	config := d.Load(EmailDomain(email), domain.WithWildcard())
	if nil == config {
		return nil, appctx.NewClientErrorf(appctx.DomainNotAllowed, nil,
			"%s has no domain configuration", email)
	}

	if !config.ValidateWhiteList(email) {
		return nil, appctx.NewClientErrorf(appctx.AccountNotAllowed, nil,
			"%s not whitelisted", email)
	}

	return config, nil
}

// EmailDomain is MailSo\Base\Utils::getEmailAddressDomain(): the part after the
// last "@", or "" when the address has none.
func EmailDomain(email string) string {
	if index := strings.LastIndex(email, "@"); 0 <= index {
		return email[index+1:]
	}
	return ""
}

// EmailLocalPart is MailSo\Base\Utils::getEmailAddressLocalPart().
func EmailLocalPart(email string) string {
	return domain.LocalPart(email)
}
