<?php
/**
 * https://datatracker.ietf.org/doc/draft-bucksch-autoconfig/
 */

namespace RainLoop\Providers\Domain;

abstract class Autoconfig
{
	/**
	 * Maximum amount of bytes we read from a remote configuration file
	 */
	const MAX_RESPONSE_SIZE = 1048576; // 1 MB

	/**
	 * Networks which we must never contact, else this
	 * functionality can be abused as a SSRF probe into
	 * the internal network (or cloud metadata services).
	 */
	const BLOCKED_IPV4_NETWORKS = [
		'0.0.0.0/8',        // "this" network
		'10.0.0.0/8',       // private
		'100.64.0.0/10',    // carrier-grade NAT
		'127.0.0.0/8',      // loopback
		'169.254.0.0/16',   // link-local (incl. 169.254.169.254 metadata)
		'172.16.0.0/12',    // private
		'192.0.0.0/24',     // IETF protocol assignments
		'192.0.2.0/24',     // TEST-NET-1
		'192.88.99.0/24',   // 6to4 relay anycast
		'192.168.0.0/16',   // private
		'198.18.0.0/15',    // benchmarking
		'198.51.100.0/24',  // TEST-NET-2
		'203.0.113.0/24',   // TEST-NET-3
		'224.0.0.0/4',      // multicast
		'240.0.0.0/4'       // reserved + broadcast
	];

	const BLOCKED_IPV6_NETWORKS = [
		'::/128',           // unspecified
		'::1/128',          // loopback
		'::ffff:0:0/96',    // IPv4-mapped
		'64:ff9b:1::/48',   // local-use NAT64
		'100::/64',         // discard-only
		'2001::/32',        // Teredo
		'2001:db8::/32',    // documentation
		'2002::/16',        // 6to4
		'fc00::/7',         // unique local
		'fe80::/10',        // link-local
		'fec0::/10',        // deprecated site-local
		'ff00::/8'          // multicast
	];

	public static function discover(string $emailaddress) : ?array
	{
		$domain = \MailSo\Base\Utils::getEmailAddressDomain($emailaddress);
		$domain = static::toAscii($domain);
		if (!static::isSafeHost($domain)) {
			\SnappyMail\Log::warning('Autoconfig', 'Refused to discover unsafe domain: '.static::logValue($domain));
			return null;
		}
		return
			// First try autoconfig
			static::resolve($domain, $emailaddress)
			// Else try MX, but it is mostly useless
//			?: static::mx($domain, $emailaddress)
			// Else try Microsoft autodiscover
			?: static::autodiscover($domain)
			// Else try DNS SRV
			?: static::srv($domain);
	}

	private static function toAscii(string $domain) : string
	{
		$domain = \trim($domain);
		return '' === $domain ? '' : \strtolower((string) \idn_to_ascii($domain));
	}

	/**
	 * User/DNS controlled values are written to the log, make them harmless
	 */
	private static function logValue(string $value) : string
	{
		return \substr(\preg_replace('/[^\x20-\x7E]/', '?', $value), 0, 120);
	}

	/**
	 * Fetch a remote URL.
	 *
	 * The host is supplied by the (admin) user and/or by DNS records,
	 * so every request is validated first: it must be a public host and
	 * redirects are not followed. This prevents Server Side Request
	 * Forgery, where the webmail server is used to probe the internal
	 * network or cloud metadata services.
	 */
	private static function fetch(string $url, array $aHttpOptions = array()) : string
	{
		$aUrl = \parse_url($url);
		if (!\is_array($aUrl)
		 || !isset($aUrl['host'])
		 || !\in_array(\strtolower($aUrl['scheme'] ?? ''), ['http', 'https'], true)
		 || isset($aUrl['user']) || isset($aUrl['pass'])
		 || !static::isSafeHost($aUrl['host'])
		) {
			// Only log the scheme and host, the URL may contain an email address
			\SnappyMail\Log::warning('Autoconfig', 'Blocked request to unsafe URL: '
				. (\is_array($aUrl) ? (\strtolower($aUrl['scheme'] ?? '?') . '://' . ($aUrl['host'] ?? '?')) : 'invalid'));
			return '';
		}

		$context = \stream_context_create([
			'http' => \array_merge([
				'method'          => 'GET',
				'timeout'         => 5,
				// A redirect could point to a host we did not validate
				'follow_location' => 0,
				'max_redirects'   => 0,
				'protocol_version'=> 1.1,
				'header'          => 'Connection: close'
			], $aHttpOptions),
			'ssl' => [
				'verify_peer'      => true,
				'verify_peer_name' => true
			]
		]);

		$data = @\file_get_contents($url, false, $context, 0, static::MAX_RESPONSE_SIZE);
		return \is_string($data) ? $data : '';
	}

	/**
	 * Hosts we already validated during this request
	 */
	private static $aHostSafetyCache = array();

	/**
	 * A host is only safe when it is a valid public DNS name or a public IP,
	 * which does not resolve to any private/reserved address.
	 * Returns false when the host can't be resolved (fail closed).
	 */
	private static function isSafeHost(string $host) : bool
	{
		$host = \strtolower(\trim($host, " \t\r\n\0\x0B"));
		// Strip the DNS root dot and IPv6 brackets
		$host = \rtrim($host, '.');
		if (\str_starts_with($host, '[') && \str_ends_with($host, ']')) {
			$host = \substr($host, 1, -1);
		}
		if (!isset(static::$aHostSafetyCache[$host])) {
			static::$aHostSafetyCache[$host] = static::checkHost($host);
		}
		return static::$aHostSafetyCache[$host];
	}

	private static function checkHost(string $host) : bool
	{
		if ('' === $host || 253 < \strlen($host)) {
			return false;
		}

		// IP literal
		if (\filter_var($host, \FILTER_VALIDATE_IP)) {
			return static::isPublicIP($host);
		}

		// Must be a proper domain name: at least two labels, no ports/paths
		if (!\preg_match('/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/D', $host)) {
			return false;
		}

		// Never contact special-use/internal-only domains
		if (\preg_match('/(?:^|\.)(?:localhost|local|localdomain|internal|intranet|lan|home|home\.arpa|corp|private|test|example|invalid|onion)$/', $host)) {
			return false;
		}

		$aIPs = static::resolveHost($host);
		if (!$aIPs) {
			return false;
		}
		foreach ($aIPs as $sIP) {
			if (!static::isPublicIP($sIP)) {
				return false;
			}
		}
		return true;
	}

	private static function resolveHost(string $host) : array
	{
		$aIPs = array();
		$records = @\dns_get_record($host, \DNS_A | \DNS_AAAA);
		if (\is_array($records)) {
			foreach ($records as $record) {
				if (!empty($record['ip'])) {
					$aIPs[] = $record['ip'];
				} else if (!empty($record['ipv6'])) {
					$aIPs[] = $record['ipv6'];
				}
			}
		}
		if (!$aIPs) {
			// Fallback to the system resolver, which also uses /etc/hosts
			foreach (@\gethostbynamel($host) ?: array() as $sIP) {
				$aIPs[] = $sIP;
			}
		}
		return \array_values(\array_unique($aIPs));
	}

	private static function isPublicIP(string $ip) : bool
	{
		$ip = \trim($ip, " \t\r\n\0\x0B[]");
		// Strip IPv6 zone index like fe80::1%eth0
		$iPos = \strpos($ip, '%');
		if (false !== $iPos) {
			$ip = \substr($ip, 0, $iPos);
		}
		// Normalize IPv4-mapped IPv6 like ::ffff:127.0.0.1
		if (\preg_match('/^::(?:ffff:)?(\d{1,3}(?:\.\d{1,3}){3})$/i', $ip, $aMatch)) {
			return static::isPublicIP($aMatch[1]);
		}
		if (!\filter_var($ip, \FILTER_VALIDATE_IP)) {
			return false;
		}
		$packed = @\inet_pton($ip);
		if (false === $packed) {
			return false;
		}
		$aNetworks = 4 === \strlen($packed)
			? static::BLOCKED_IPV4_NETWORKS
			: static::BLOCKED_IPV6_NETWORKS;
		foreach ($aNetworks as $network) {
			if (static::ipInNetwork($packed, $network)) {
				return false;
			}
		}
		return true;
	}

	private static function ipInNetwork(string $packed, string $network) : bool
	{
		$aParts = \explode('/', $network, 2);
		$packedNetwork = @\inet_pton($aParts[0]);
		if (false === $packedNetwork || \strlen($packedNetwork) !== \strlen($packed)) {
			return false;
		}
		$iBits = (int) ($aParts[1] ?? 0);
		$iBytes = \intdiv($iBits, 8);
		if (0 < $iBytes && \substr($packed, 0, $iBytes) !== \substr($packedNetwork, 0, $iBytes)) {
			return false;
		}
		$iRest = $iBits % 8;
		if (0 < $iRest) {
			$iMask = (0xFF << (8 - $iRest)) & 0xFF;
			if ((\ord($packed[$iBytes]) & $iMask) !== (\ord($packedNetwork[$iBytes]) & $iMask)) {
				return false;
			}
		}
		return true;
	}

	private static function resolve(string $domain, string $emailaddress) : ?array
	{
		if (!static::isSafeHost($domain)) {
			return null;
		}
		$emailaddress = \urlencode($emailaddress);
		foreach ([
			// Mail provider https://www.ietf.org/archive/id/draft-bucksch-autoconfig-02.html#section-4.1
			"https://autoconfig.{$domain}/.well-known/mail-v1.xml?emailaddress={$emailaddress}",
			"https://{$domain}/.well-known/autoconfig/mail/config-v1.1.xml",
			"http://autoconfig.{$domain}/mail/config-v1.1.xml",
			// Central database https://www.ietf.org/archive/id/draft-bucksch-autoconfig-02.html#section-4.2
			"https://autoconfig.thunderbird.net/v1.1/{$domain}"
		] as $url) {
			$data = static::fetch($url);
			if ($data && \str_contains($data, '<clientConfig')) {
				$data = \json_decode(
					\json_encode(
						\simplexml_load_string($data, 'SimpleXMLElement', \LIBXML_NOCDATA)
					), true);
				if (!empty($data['emailProvider'])) {
					$data = $data['emailProvider'];
					unset($data['documentation']);
					$data['incomingServer'] = \array_filter(
						isset($data['incomingServer'][0]) ? $data['incomingServer'] : [$data['incomingServer']],
						fn($data) => 'imap' === $data['@attributes']['type']
					);
					$data['outgoingServer'] = \array_filter(
						isset($data['outgoingServer'][0]) ? $data['outgoingServer'] : [$data['outgoingServer']],
						fn($data) => 'smtp' === $data['@attributes']['type']
					);
					$data['canonical'] = $url;
					return $data;
				}
			}
		}
		return null;
	}

	private static function publicsuffixes() : array
	{
		$oCache = \RainLoop\Api::Actions()->Cacher();
		$list = $oCache->Get('public_suffix_list');
		if ($list) {
			$list = \json_decode($list, true);
			if ($list[1] < \time()) {
				$list = null;
			} else {
				$list = $list[0];
			}
		}
		if (null === $list) {
			$list = [];
			$data = static::fetch('https://publicsuffix.org/list/public_suffix_list.dat');
			if ($data) {
				$list = \array_filter(
					\explode("\n", $data),
					fn($text) => \strlen($text) && '/' !== $text[0] && '*' !== $text[0] && \substr_count($text, '.')
				);
			}
			// Don't lookup for 24 hours
			$oCache->Set('public_suffix_list', \json_encode([$list, time() + 86400]));
		}
		return $list ?: [];
	}

	/**
	 * https://www.ietf.org/archive/id/draft-bucksch-autoconfig-02.html#section-4.3
	 */
	private static function mx(string $domain, string $emailaddress) : ?array
	{
		$suffixes = static::publicsuffixes();
		$hostnames = [];
		foreach (\SnappyMail\DNS::MX($domain) as $hostname) {
			// Extract only the second-level domain from the MX hostname
			$mxbasedomain = \explode('.', $hostname);
			$i = -2;
			while (\in_array(\implode('.', \array_slice($mxbasedomain, $i)), $suffixes)) {
				--$i;
			}
			$mxbasedomain = \implode('.', \array_slice($mxbasedomain, $i));
			if ($mxbasedomain) {
				$mxfulldomain = $mxbasedomain;
				if (\substr_count($hostname, '.') > \substr_count($mxbasedomain, '.')) {
					// Remove the first component from the MX hostname
					$mxfulldomain = \explode('.', $hostname, 2)[1];
				}
				$hostnames[$mxfulldomain] = $mxbasedomain;
			}
		}
		foreach ($hostnames as $mxfulldomain => $mxbasedomain) {
			if ($domain != $mxfulldomain) {
				$autoconfig = static::resolve($mxfulldomain, $emailaddress);
				if (!$autoconfig && $mxfulldomain != $mxbasedomain && $domain != $mxbasedomain) {
					$autoconfig = static::resolve($mxbasedomain, $emailaddress);
				}
				if ($autoconfig) {
					return $autoconfig;
				}
			}
		}
		return null;
	}

	/**
	 * https://datatracker.ietf.org/doc/html/rfc6186
	 * https://datatracker.ietf.org/doc/html/rfc8314
	 */
	private static function srv(string $domain) : ?array
	{
		$srv = \SnappyMail\DNS::SRV('_submissions._tcp.'.$domain);
		if (empty($srv[0]['target'])) {
			$srv = \SnappyMail\DNS::SRV('_submission._tcp.'.$domain);
		}
		if (!empty($srv[0]['target'])) {
			$result = [
				'incomingServer' => [],
				'outgoingServer' => [
					'hostname' => $srv[0]['target'],
					'port' => $srv[0]['port'],
					'socketType' => (587 == $srv[0]['port']) ? 'STARTTLS' : (465 == $srv[0]['port'] ? 'SSL' : ''),
					'authentication' => 'password-cleartext',
					'username' => '%EMAILADDRESS%'
				]
			];
			$srv = \SnappyMail\DNS::SRV('_imaps._tcp.'.$domain);
			if (empty($srv[0]['target'])) {
				$srv = \SnappyMail\DNS::SRV('_imap._tcp.'.$domain);
			}
			if (!empty($srv[0]['target'])) {
				$result['incomingServer'][] = [
					'hostname' => $srv[0]['target'],
					'port' => $srv[0]['port'],
					'socketType' => 993 == $srv[0]['port'] ? 'SSL' : '',
					'authentication' => 'password-cleartext',
					'username' => '%EMAILADDRESS%'
				];
				return $result;
			}
		}
		return null;
	}

	/**
	 * This is Microsoft
	 * https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/pox-autodiscover-web-service-reference-for-exchange
	 */
	private static function autodiscover(string $domain) : ?array
	{
		if (!static::isSafeHost($domain)) {
			return null;
		}
		foreach (\SnappyMail\DNS::SRV("_autodiscover._tcp.{$domain}") as $record) {
			if (empty($record['target'])) {
				continue;
			}
			// The SRV target is remote controlled, so it must be validated
			$target = \rtrim(\trim($record['target']), '.');
			if (!static::isSafeHost($target)) {
				\SnappyMail\Log::warning('Autoconfig', 'Blocked autodiscover SRV target: '.static::logValue($target));
				continue;
			}
			if (443 == $record['port']) {
				$result = static::autodiscover_resolve("https://{$target}", $domain);
			} else if (80 == $record['port']) {
				$result = static::autodiscover_resolve("http://{$target}", $domain);
			} else {
				$result = static::autodiscover_resolve("https://{$target}:{$record['port']}", $domain);
			}
			if ($result) {
				return $result;
			}
		}
		foreach ([
			"https://{$domain}",
			"https://autodiscover.{$domain}",
			"http://autodiscover.{$domain}"
		] as $host) {
			$result = static::autodiscover_resolve($host, $domain);
			if ($result) {
				return $result;
			}
		}
		return null;
	}

	private static function autodiscover_resolve(string $host, string $domain) : ?array
	{
		$email = "autodiscover@{$domain}";
		$url = "{$host}/autodiscover/autodiscover.xml";
		$body = static::autodiscover_request($email);
		$xml = static::fetch($url, [
			'method'  => 'POST',
			'header'  => "Content-Type: application/xml\r\nContent-Length: " . \strlen($body) . "\r\nConnection: close",
			'content' => $body
		]);
		if ($xml) {
			$data = \json_decode(
				\json_encode(
					\simplexml_load_string($xml, 'SimpleXMLElement', \LIBXML_NOCDATA)
				), true);
			if (!empty($data['Response']['Account']['Protocol']) && 'email' === $data['Response']['Account']['AccountType']) {
				$result = [
					'incomingServer' => [],
					'outgoingServer' => []
				];
				foreach ($data['Response']['Account']['Protocol'] as $entry) {
					if ('IMAP' === $entry['Type']) {
						$result['incomingServer'][] = [
							'hostname' => $entry['Server'],
							'port' => $entry['Port'],
							'socketType' => ('on' === $entry['SSL']) ? (993 == $entry['Port'] ? 'SSL' : 'STARTTLS') : '',
							'authentication' => 'password-cleartext',
							'username' => ($entry['LoginName'] === $email) ? '%EMAILADDRESS%' : ''
						];
					} else if ('SMTP' === $entry['Type']) {
						$result['outgoingServer'][] = [
							'hostname' => $entry['Server'],
							'port' => $entry['Port'],
							'socketType' => (587 == $entry['Port']) ? 'STARTTLS' : ('on' === $entry['SSL'] ? 'SSL' : ''),
							'authentication' => 'password-cleartext',
							'username' => ($entry['LoginName'] === $email) ? '%EMAILADDRESS%' : ''
						];
					}
				}
				$result['canonical'] = $url;
				return $result;
			}
		}
		return null;
	}

	private static function autodiscover_request(string $email) : string
	{
		return '<?xml version="1.0" encoding="utf-8" ?>
		<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006">
			<Request>
				<AcceptableResponseSchema>http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a</AcceptableResponseSchema>
				<EMailAddress>'.$email.'</EMailAddress>
			</Request>
		</Autodiscover>';
	}

}
