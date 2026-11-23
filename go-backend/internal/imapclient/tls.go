package imapclient

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// sslTransportsSupported mirrors the "in_array('ssl', stream_get_transports())"
// check of ConnectionSecurityType::UseSSL(). A Go binary always has TLS, so the
// fallback to a plain connection never happens.
const sslTransportsSupported = true

// useSSL is ConnectionSecurityType::UseSSL(): implicit TLS for SSL (1) and for
// AUTO_DETECT (9) on the well known TLS ports 993/995/465.
//
// The check is deliberately limited to those three ports, exactly like PHP: a
// custom port with AUTO_DETECT stays plain and is upgraded with STARTTLS when
// the server advertises it.
func useSSL(port, securityType int) bool {
	result := securityType
	if domain.SecurityAutoDetect == securityType {
		switch port {
		case 993, 995, 465:
			result = domain.SecuritySSL
		}
	}

	if domain.SecuritySSL != result {
		return false
	}
	return sslTransportsSupported
}

// tlsConfiguration builds a crypto/tls config from MailSo's SSLContext (the
// [ssl] section of application.ini, copied into every domain config).
//
// The mapping is faithful where Go allows it:
//
//   - verify_peer / allow_self_signed: Go cannot "verify the chain but accept
//     self signed leaves", so either flag disables verification
//     (InsecureSkipVerify). This is documented as a deviation;
//   - cafile / capath: loaded into RootCAs (a directory is read as ordinary PEM
//     files, because Go has no hashed OpenSSL directory lookup);
//   - verify_peer with verify_peer_name off: the chain is verified through a
//     custom VerifyConnection that skips the host name check;
//   - a local_cert is loaded as the client certificate (the file holds both the
//     certificate and the private key, like PHP's ssl context expects);
//   - TLS 1.2 is the minimum, matching NetClient::EnableCrypto().
func tlsConfiguration(settings *domain.SSL, host string) (*tls.Config, error) {
	config := &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}

	if nil == settings {
		return config, nil
	}

	if !settings.VerifyPeer || settings.AllowSelfSigned {
		config.InsecureSkipVerify = true
	}

	// settings.DisableCompression is moot: Go never negotiates TLS compression,
	// so there is nothing to switch off (PHP's stream context needs the flag).

	pool, err := rootPool(settings)
	if nil != err {
		return nil, err
	}
	if nil != pool {
		config.RootCAs = pool
	}

	// verify_peer with verify_peer_name off: verify the chain but not the host.
	if settings.VerifyPeer && !settings.VerifyPeerName {
		roots := pool
		if nil == roots {
			if system, systemErr := x509.SystemCertPool(); nil == systemErr {
				roots = system
			}
		}
		if nil != roots {
			config.InsecureSkipVerify = true
			config.VerifyConnection = func(state tls.ConnectionState) error {
				if 0 == len(state.PeerCertificates) {
					return x509.UnknownAuthorityError{}
				}
				intermediates := x509.NewCertPool()
				for _, certificate := range state.PeerCertificates[1:] {
					intermediates.AddCert(certificate)
				}
				_, verifyErr := state.PeerCertificates[0].Verify(x509.VerifyOptions{
					Roots:         roots,
					Intermediates: intermediates,
				})
				return verifyErr
			}
		}
	}

	if "" != strings.TrimSpace(settings.LocalCert) {
		certificate, loadErr := tls.LoadX509KeyPair(settings.LocalCert, settings.LocalCert)
		if nil != loadErr {
			return nil, loadErr
		}
		config.Certificates = []tls.Certificate{certificate}
	}

	return config, nil
}

// rootPool builds the CA pool from cafile and capath. It returns nil when
// neither is configured, so the caller keeps Go's system trust store.
func rootPool(settings *domain.SSL) (*x509.CertPool, error) {
	file := strings.TrimSpace(settings.CAFile)
	directory := strings.TrimSpace(settings.CAPath)
	if "" == file && "" == directory {
		return nil, nil
	}

	pool := x509.NewCertPool()

	if "" != file {
		if err := appendCertFile(pool, file); nil != err {
			return nil, err
		}
	}

	if "" != directory {
		entries, err := os.ReadDir(directory)
		if nil != err {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			// A single unreadable/non-certificate file must not abort the whole
			// pool, mirroring OpenSSL's best effort directory hashing.
			_ = appendCertFile(pool, filepath.Join(directory, entry.Name()))
		}
	}

	return pool, nil
}

// appendCertFile adds the certificates of one PEM file to the pool.
func appendCertFile(pool *x509.CertPool, file string) error {
	content, err := os.ReadFile(file)
	if nil != err {
		return err
	}
	pool.AppendCertsFromPEM(content)
	return nil
}
