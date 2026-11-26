package handler

// The domain administration actions of \RainLoop\Actions\AdminDomains (Step 27):
// load/list/delete/disable/save/alias-save, match and the connectivity /
// autoconfig helpers. The domain files are the canonical
// data/_data_/<name>/domains/<name>.json written by internal/domain, so a domain
// created here is byte-identical to one PHP writes.
//
// The connectivity probe (DoAdminDomainTest) and the remote autoconfiguration
// discovery (DoAdminDomainAutoconfig) are partial: the Go backend does not open
// IMAP/SMTP/Sieve sessions or resolve DNS records from the admin panel.

import (
	"net"
	"os"
	"regexp"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// adminDomainNamePattern is the FILTER_VALIDATE regex of
// DoAdminDomainAutoconfig().
var adminDomainNamePattern = regexp.MustCompile(
	`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

// RegisterAdminDomains registers the DoAdminDomain* actions.
func RegisterAdminDomains(d *Dispatcher) {
	d.Register("AdminDomainLoad", doAdminDomainLoad)
	d.Register("AdminDomainList", doAdminDomainList)
	d.Register("AdminDomainDelete", doAdminDomainDelete)
	d.Register("AdminDomainDisable", doAdminDomainDisable)
	d.Register("AdminDomainSave", doAdminDomainSave)
	d.Register("AdminDomainAliasSave", doAdminDomainAliasSave)
	d.Register("AdminDomainMatch", doAdminDomainMatch)
	d.Register("AdminDomainTest", doAdminDomainTest)
	d.Register("AdminDomainAutoconfig", doAdminDomainAutoconfig)
}

// adminDomains is DomainProvider(), or nil when unwired.
func (c *Context) adminDomains() *domain.Provider {
	if nil == c || nil == c.dispatcher {
		return nil
	}
	return c.dispatcher.domains
}

// doAdminDomainLoad is Actions\AdminDomains::DoAdminDomainLoad().
func doAdminDomainLoad(ctx *Context) (any, error) {
	provider := ctx.adminDomains()
	if nil == provider {
		return ctx.FalseResponse(0, "", ""), nil
	}

	config := provider.Load(ctx.stringParam("name", ""),
		domain.WithoutDisabledCheck(), domain.WithoutAliasCheck())
	if nil == config {
		return ctx.FalseResponse(0, "", ""), nil
	}

	result := config.JSONSerialize()
	result["name"] = config.Name

	return ctx.DefaultResponse(result, nil), nil
}

// doAdminDomainList is Actions\AdminDomains::DoAdminDomainList().
func doAdminDomainList(ctx *Context) (any, error) {
	provider := ctx.adminDomains()
	if nil == provider {
		return ctx.DefaultResponse([]any{}, nil), nil
	}

	includeAliases := phpTruthy(ctx.GetParam("includeAliases", "1"))

	entries := provider.GetList(includeAliases)
	list := make([]any, 0, len(entries))
	for _, entry := range entries {
		list = append(list, map[string]any{
			"name":     entry.Name,
			"disabled": entry.Disabled,
			"alias":    entry.Alias,
		})
	}

	return ctx.DefaultResponse(list, nil), nil
}

// doAdminDomainDelete is Actions\AdminDomains::DoAdminDomainDelete().
func doAdminDomainDelete(ctx *Context) (any, error) {
	provider := ctx.adminDomains()
	if nil == provider {
		return ctx.FalseResponse(0, "", ""), nil
	}

	deleted, err := provider.Delete(ctx.stringParam("name", ""))
	if nil != err {
		ctx.dispatcher.logException(err)
	}

	return ctx.DefaultResponse(deleted, nil), nil
}

// doAdminDomainDisable is Actions\AdminDomains::DoAdminDomainDisable().
func doAdminDomainDisable(ctx *Context) (any, error) {
	provider := ctx.adminDomains()
	if nil == provider {
		return ctx.FalseResponse(0, "", ""), nil
	}

	disabled, err := provider.Disable(
		ctx.stringParam("name", ""),
		!phpEmpty(ctx.GetParam("disabled", "0")),
	)
	if nil != err {
		ctx.dispatcher.logException(err)
	}

	return ctx.DefaultResponse(disabled, nil), nil
}

// doAdminDomainSave is Actions\AdminDomains::DoAdminDomainSave().
func doAdminDomainSave(ctx *Context) (any, error) {
	provider := ctx.adminDomains()
	if nil == provider {
		return ctx.FalseResponse(0, "", ""), nil
	}

	config, err := ctx.domainFromAction("")
	if nil != err {
		return nil, err
	}
	if nil == config {
		return ctx.FalseResponse(0, "", ""), nil
	}

	if err := provider.Save(config); nil != err {
		ctx.dispatcher.logException(err)
		return ctx.FalseResponse(0, "", ""), nil
	}

	return ctx.DefaultResponse(true, nil), nil
}

// doAdminDomainAliasSave is Actions\AdminDomains::DoAdminDomainAliasSave().
func doAdminDomainAliasSave(ctx *Context) (any, error) {
	provider := ctx.adminDomains()
	if nil == provider {
		return ctx.FalseResponse(0, "", ""), nil
	}

	name := ctx.stringParam("name", "")
	// Providers\Domain::SaveAlias(): the alias target must not already exist.
	if nil != provider.Load(name, domain.WithoutDisabledCheck()) {
		return nil, clientError(appctx.DomainAlreadyExists, "")
	}

	if err := provider.SaveAlias(name, ctx.stringParam("alias", "")); nil != err {
		ctx.dispatcher.logException(err)
		return ctx.FalseResponse(0, "", ""), nil
	}

	return ctx.DefaultResponse(true, nil), nil
}

// doAdminDomainMatch is Actions\AdminDomains::DoAdminDomainMatch(): the login
// preview of the domain matching, without a real login.
func doAdminDomainMatch(ctx *Context) (any, error) {
	email, config := ctx.resolveDomainMatch(ctx.stringParam("username", ""))

	var name, whitelist any
	if nil != config {
		name = config.Name
		whitelist = config.ValidateWhiteList(email)
	}

	return ctx.DefaultResponse(map[string]any{
		"email":     email,
		"login":     ctx.domainMatchLogin(email, config),
		"name":      name,
		"whitelist": whitelist,
	}, nil), nil
}

// domainMatchLogin is the ImapSettings::fixUsername($email) of resolveLoginCredentials().
func (c *Context) domainMatchLogin(email string, config *domain.Domain) string {
	if nil == config {
		return email
	}
	return config.IMAP.FixUsername(email, true)
}

// doAdminDomainTest is Actions\AdminDomains::DoAdminDomainTest().
//
// Partial: the Go backend does not open IMAP/SMTP/Sieve sessions from the admin
// panel, so the connection results are reported as unsupported while the domain
// config is still validated (and the Sieve-disabled case mirrors PHP).
func doAdminDomainTest(ctx *Context) (any, error) {
	config, err := ctx.domainFromAction("test.example.com")
	if nil != err {
		return nil, err
	}

	sieveResult := false
	sieveMessage := ""
	if nil != config {
		// PHP sets $mSieveResult = true when Sieve is disabled for the domain.
		if !config.Sieve.Enabled {
			sieveResult = true
		} else {
			sieveMessage = "Sieve connection test is not available in the Go backend"
		}
	}

	imapValue := "IMAP connection test is not available in the Go backend"
	smtpValue := "SMTP connection test is not available in the Go backend"
	if nil == config {
		imapValue = ""
		smtpValue = ""
	}

	sieveValue := any(sieveMessage)
	if sieveResult {
		sieveValue = true
	}

	return ctx.DefaultResponse(map[string]any{
		"Imap":        imapValue,
		"Smtp":        smtpValue,
		"Sieve":       sieveValue,
		"ImapResult":  false,
		"SmtpResult":  false,
		"SieveResult": sieveResult,
	}, nil), nil
}

// doAdminDomainAutoconfig is Actions\AdminDomains::DoAdminDomainAutoconfig().
//
// Partial: only the client supplied domain is validated; the remote
// Autoconfig::discover() lookup (DNS/MX/SRV/autodiscover) is not ported, so the
// "config" is null.
func doAdminDomainAutoconfig(ctx *Context) (any, error) {
	raw := ctx.GetParam("domain", "")
	value, isScalar := raw.(string)
	if !isScalar {
		return nil, clientError(appctx.InvalidInputArgument, "Invalid domain")
	}

	name := strings.TrimRight(domain.EmailToASCII(strings.TrimSpace(value)), ".")
	if "" == name || nil != net.ParseIP(name) || !adminDomainNamePattern.MatchString(name) {
		return nil, clientError(appctx.InvalidInputArgument, "Invalid domain")
	}

	return ctx.DefaultResponse(map[string]any{
		"email":  "test@" + name,
		"config": nil,
	}, nil), nil
}

// domainFromAction is Providers\Domain::LoadOrCreateNewFromAction().
//
//	nameForTest is PHP's $sNameForTest (a fixed name for the connection test).
//	An existing domain with "create" set is DomainAlreadyExists.
func (c *Context) domainFromAction(nameForTest string) (*domain.Domain, error) {
	provider := c.adminDomains()
	if nil == provider {
		return nil, nil
	}

	name := strings.ToLower(c.stringParam("name", ""))
	if "" != name && "" != nameForTest && !strings.Contains(name, "*") {
		nameForTest = ""
	}
	if "" == name && "" == nameForTest {
		return nil, nil
	}

	if "" == nameForTest && !phpEmpty(c.GetParam("create", 0)) && nil != provider.Load(name) {
		return nil, clientError(appctx.DomainAlreadyExists, "")
	}

	target := name
	if "" != nameForTest {
		target = nameForTest
	}

	value := map[string]any{
		"IMAP":      c.GetParam("IMAP", nil),
		"SMTP":      c.GetParam("SMTP", nil),
		"Sieve":     c.GetParam("Sieve", nil),
		"whiteList": c.GetParam("whiteList", nil),
	}

	return provider.NewFromArray(target, value), nil
}

// resolveDomainMatch is the resolveLoginCredentials() subset used by
// DoAdminDomainMatch(): apply determine_user_domain/default_domain, then load the
// domain and normalise the address. Plugin hooks and IMAP are skipped.
func (c *Context) resolveDomainMatch(username string) (string, *domain.Domain) {
	email := domain.EmailToASCII(strings.TrimSpace(username))

	if !strings.Contains(email, "@") {
		if configured := strings.TrimSpace(c.Config().GetString("login", "default_domain", "")); "" != configured {
			switch configured {
			case "HTTP_HOST", "SERVER_NAME":
				if nil != c.Request {
					configured = stripPort(c.Request.Host)
				}
			case "gethostname":
				if hostname, err := os.Hostname(); nil == err {
					configured = hostname
				}
			}
			if "" != configured {
				email += "@" + configured
			}
		}
	}

	provider := c.adminDomains()
	if nil == provider || !strings.Contains(email, "@") {
		return email, nil
	}

	config := provider.Load(domainOf(email), domain.WithWildcard())
	if nil == config {
		return email, nil
	}

	return config.IMAP.FixUsername(email, false), config
}

// domainOf is MailSo\Base\Utils::getEmailAddressDomain().
func domainOf(email string) string {
	if index := strings.LastIndex(email, "@"); 0 <= index {
		return email[index+1:]
	}
	return ""
}

// stripPort removes a trailing ":port" from a host name.
func stripPort(host string) string {
	if index := strings.LastIndex(host, ":"); 0 <= index && !strings.Contains(host[index:], "]") {
		return host[:index]
	}
	return host
}
