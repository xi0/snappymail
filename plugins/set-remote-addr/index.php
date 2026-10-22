<?php
/**
 * Can be used for proxies, like Nginx  with:
 *     proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
 * It's an alternative to application.ini http_client_ip_check_proxy
 *
 * For safety, forwarded headers are only trusted when the direct peer
 * (REMOTE_ADDR) is listed in the "trusted_proxies" plugin setting.
 */

class SetRemoteAddrPlugin extends \RainLoop\Plugins\AbstractPlugin
{
	const
		NAME = 'Set REMOTE_ADDR',
		VERSION = '2.1',
		DESCRIPTION = 'Sets the $_SERVER[\'REMOTE_ADDR\'] value from HTTP_CLIENT_IP/HTTP_X_FORWARDED_FOR, only when the request comes from a trusted proxy';

	public function Init() : void
	{
		$sRemoteAddr = \trim((string) ($_SERVER['REMOTE_ADDR'] ?? ''));
		$aTrustedProxies = \array_filter(\array_map('trim',
			\explode(',', (string) $this->Config()->Get('plugin', 'trusted_proxies', ''))));

		if (!\strlen($sRemoteAddr) || !$this->isTrustedProxy($sRemoteAddr, $aTrustedProxies)) {
			return;
		}

		$sIp = $this->getClientIp($aTrustedProxies);
		if (\strlen($sIp) && $sIp !== $sRemoteAddr) {
			$_SERVER['REMOTE_ADDR'] = $sIp;
		}
	}

	/**
	 * Return the client IP from the forwarded headers.
	 *
	 * X-Forwarded-For is an append-only chain. Walk from right (the trusted
	 * proxy) to left and skip all trusted proxies, so a client supplied value
	 * at the start of the list can not spoof the logged client address.
	 */
	private function getClientIp(array $aTrustedProxies) : string
	{
		foreach (array('HTTP_X_FORWARDED_FOR', 'HTTP_CLIENT_IP') as $sKey) {
			$mValue = $_SERVER[$sKey] ?? '';
			if (!\is_string($mValue)) {
				continue;
			}

			foreach (\array_reverse(\explode(',', $mValue)) as $sIp) {
				$sIp = \trim($sIp);
				if (\MailSo\Base\Utils::ValidateIP($sIp)
				 && !$this->isTrustedProxy($sIp, $aTrustedProxies)) {
					return $sIp;
				}
			}
		}

		return '';
	}

	/**
	 * Check whether an IP address is inside one of the configured IP/CIDR ranges.
	 */
	private function isTrustedProxy(string $sIp, array $aTrustedProxies) : bool
	{
		$sBinIp = \inet_pton($sIp);
		if (false === $sBinIp) {
			return false;
		}

		$iIpBits = \strlen($sBinIp) * 8;
		foreach ($aTrustedProxies as $sRange) {
			$iMask = $iIpBits;
			if (\str_contains($sRange, '/')) {
				list($sRange, $iMask) = \explode('/', $sRange, 2);
				$iMask = (int) $iMask;
			}

			$sBinRange = \inet_pton(\trim($sRange));
			if (false === $sBinRange
			 || \strlen($sBinRange) !== \strlen($sBinIp)
			 || $iMask < 0 || $iMask > $iIpBits) {
				continue;
			}

			$iBytes = \intdiv($iMask, 8);
			$iRemainder = $iMask % 8;
			if (\substr($sBinIp, 0, $iBytes) !== \substr($sBinRange, 0, $iBytes)) {
				continue;
			}
			if ($iRemainder) {
				$iMaskByte = (0xFF << (8 - $iRemainder)) & 0xFF;
				if ((\ord($sBinIp[$iBytes]) & $iMaskByte) !== (\ord($sBinRange[$iBytes]) & $iMaskByte)) {
					continue;
				}
			}

			return true;
		}

		return false;
	}

	protected function configMapping() : array
	{
		return array(
			\RainLoop\Plugins\Property::NewInstance('trusted_proxies')
				->SetLabel('Trusted proxies')
				->SetType(\RainLoop\Enumerations\PluginPropertyType::STRING_TEXT)
				->SetDescription('Comma separated list of trusted proxy IP addresses or CIDR ranges. Forwarded headers are only used when REMOTE_ADDR matches one of these entries.')
				->SetDefaultValue('127.0.0.1,::1')
		);
	}
}
