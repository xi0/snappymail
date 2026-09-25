<?php

class CaldavPlugin extends \RainLoop\Plugins\AbstractPlugin
{
	const
		NAME     = 'Mailbux CalDAV Auto',
		VERSION  = '1.17',
		RELEASE  = '2026-01-16',
		CATEGORY = 'Calendar',
		DESCRIPTION = 'Auto-configures CalDAV calendar sync with JMAP support - switches per account',
		REQUIRED = '2.0.0';

	private $lastConfiguredEmail = null;

	private static $aTranslations = [];

	public function Init() : void
	{
		// Load plugin translations from the langs folder (English + Danish)
		$this->UseLangs(true);

		// Self-configure CalDAV sync per account (no CardDAV dependency)
		$this->addHook('login.success', 'AutoConfigureCalDAV');
		$this->addHook('json.after-AccountSwitch', 'OnAfterAccountSwitch');

		// Add custom JSON actions
		$this->addJsonHook('GetCalendars', 'DoGetCalendars');
		$this->addJsonHook('GetCalendarEvents', 'DoGetCalendarEvents');
		$this->addJsonHook('CreateCalendarEvent', 'DoCreateCalendarEvent');
		$this->addJsonHook('UpdateCalendarEvent', 'DoUpdateCalendarEvent');
		$this->addJsonHook('DeleteCalendarEvent', 'DoDeleteCalendarEvent');

		// Calendar invites received as .ics attachments in mail messages
		$this->addJsonHook('ImportCalendarEvent', 'DoImportCalendarEvent');
		$this->addJsonHook('RespondToEvent', 'DoRespondToEvent');
		$this->addJsonHook('RemoveCalendarEvent', 'DoRemoveCalendarEvent');

		// Add JavaScript
		// meeting.js and openstreetmap.js must load first: they expose
		// window.MailbuxCalDavMeeting / window.MailbuxCalDavOsm used by both the
		// calendar dialog and the invite box.
		$this->addJs('meeting.js');
		$this->addJs('openstreetmap.js');
		$this->addJs('calendar-dialog.js');
		$this->addJs('message.js');

		// Add CSS
		$this->addCss('calendar.css');
	}
	
	/**
	 * Plugin configuration mapping
	 */
	protected function configMapping() : array
	{
		return array(
			\RainLoop\Plugins\Property::NewInstance('caldav_server')
				->SetLabel('CalDAV Server URL')
				->SetType(\RainLoop\Enumerations\PluginPropertyType::STRING)
				->SetDescription('CalDAV server URL (e.g., https://my.mailbux.com/dav/cal)')
				->SetDefaultValue('https://my.mailbux.com/dav/cal'),
			\RainLoop\Plugins\Property::NewInstance('jmap_server')
				->SetLabel('JMAP Server URL')
				->SetType(\RainLoop\Enumerations\PluginPropertyType::STRING)
				->SetDescription('JMAP server URL (e.g., https://my.mailbux.com/jmap)')
				->SetDefaultValue('https://my.mailbux.com/jmap'),
			\RainLoop\Plugins\Property::NewInstance('default_protocol')
				->SetLabel('Default Protocol')
				->SetType(\RainLoop\Enumerations\PluginPropertyType::SELECTION)
				->SetDescription('Default protocol to use for calendar sync')
				->SetOptions(['caldav', 'jmap'])
				->SetDefaultValue('caldav'),
			\RainLoop\Plugins\Property::NewInstance('auto_sync')
				->SetLabel('Auto Sync')
				->SetType(\RainLoop\Enumerations\PluginPropertyType::BOOL)
				->SetDescription('Automatically sync calendar on login and account switch')
				->SetDefaultValue(true),
			\RainLoop\Plugins\Property::NewInstance('sync_interval')
				->SetLabel('Sync Interval (minutes)')
				->SetType(\RainLoop\Enumerations\PluginPropertyType::INT)
				->SetDescription('Auto-sync interval in minutes (0 to disable)')
				->SetDefaultValue(5),
			\RainLoop\Plugins\Property::NewInstance('allow_invites')
				->SetLabel('Calendar invites')
				->SetType(\RainLoop\Enumerations\PluginPropertyType::BOOL)
				->SetDescription('Show "Add to calendar" and response options when a received mail contains an .ics calendar invite')
				->SetDefaultValue(true)
		);
	}

	/**
	 * Returns a translated string from the plugin's own langs/ folder.
	 *
	 * Only Danish ('da') has a dedicated translation; every other language falls
	 * back to English. Values are loaded from langs/en.json and, for Danish,
	 * langs/da.json (merged over the English defaults).
	 *
	 * @param string $sKey      key inside the "CALDAV" namespace
	 * @param array  $aReplace  placeholder replacements, e.g. ['CODE' => 204]
	 * @param string $sDefault  fallback when the key is missing
	 */
	private function msg(string $sKey, array $aReplace = [], string $sDefault = '') : string
	{
		$sLang = 'en';
		if ($this->Manager()) {
			$sLang = \strtolower(\substr((string) $this->Manager()->Actions()->GetLanguage(), 0, 2));
			$sLang = ('da' === $sLang) ? 'da' : 'en';
		}

		if (!isset(self::$aTranslations[$sLang])) {
			$aValues = [];
			foreach (['en', $sLang] as $sFileLang) {
				$sFile = $this->Path() . '/langs/' . $sFileLang . '.json';
				if (\is_file($sFile)) {
					$aData = \json_decode((string) \file_get_contents($sFile), true);
					if (isset($aData['CALDAV']) && \is_array($aData['CALDAV'])) {
						$aValues = \array_replace($aValues, $aData['CALDAV']);
					}
				}
			}
			self::$aTranslations[$sLang] = $aValues;
		}

		$sText = self::$aTranslations[$sLang][$sKey] ?? ($sDefault ?: $sKey);
		foreach ($aReplace as $sName => $sValue) {
			$sText = \str_replace('%' . $sName . '%', (string) $sValue, $sText);
		}

		return $sText;
	}

	/**
	 * Called after AccountSwitch action completes.
	 */
	public function OnAfterAccountSwitch(array &$aResponse)
	{
		if (!empty($aResponse['Result'])) {
			// Account switch succeeded - clear last email to force update
			$this->lastConfiguredEmail = null;

			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if ($oAccount) {
				$this->AutoConfigureCalDAV($oAccount);
			}
		}
	}

	/**
	 * Auto-configure CalDAV sync for the current account using this plugin's
	 * own settings (caldav_server) and its own storage namespace (calendar_sync).
	 */
	public function AutoConfigureCalDAV(\RainLoop\Model\Account $oAccount)
	{
		if (!$oAccount || !$oAccount->Email()) {
			return;
		}

		if (!$this->Config()->Get('plugin', 'auto_sync', true)) {
			return;
		}

		$sEmail = $oAccount->Email();

		// Only update if email changed (avoid updating on every request)
		if ($this->lastConfiguredEmail === $sEmail) {
			return;
		}

		$this->lastConfiguredEmail = $sEmail;
		$oActions = $this->Manager()->Actions();

		try {
			$oStorageProvider = $oActions->StorageProvider();
			if (!$oStorageProvider) {
				return;
			}

			// Build the CalDAV home URL from this plugin's own setting
			$sServer = \trim($this->Config()->Get('plugin', 'caldav_server', 'https://my.mailbux.com/dav/cal'));
			if ('' === $sServer) {
				return;
			}
			$sCalDAVUrl = \rtrim($sServer, '/') . '/' . $sEmail;

			// Get account credentials
			$sPassword = null;
			$sPasswordHMAC = null;

			$aAdditionalAccounts = $this->getAdditionalAccounts($oAccount, $oStorageProvider);

			if (isset($aAdditionalAccounts[$sEmail]['pass'])) {
				// Added account - convert password format
				$oMainAccount = $oActions->GetMainAccountFromToken();
				if (!$oMainAccount) {
					return;
				}

				$sCryptKey = $oMainAccount->CryptKey();

				$sRawPassword = \SnappyMail\Crypt::DecryptUrlSafe($aAdditionalAccounts[$sEmail]['pass'], $sCryptKey);
				if (is_object($sRawPassword) && method_exists($sRawPassword, '__toString')) {
					$sRawPassword = (string)$sRawPassword;
				}
				if (!$sRawPassword) {
					return;
				}

				$sPassword = \SnappyMail\Crypt::EncryptToJSON($sRawPassword, $sCryptKey);
				$sPasswordHMAC = \hash_hmac('sha1', $sPassword, $sCryptKey);
			} else {
				// Primary account - encrypt password
				$sRawPassword = $oAccount->ImapPass();
				$sCryptKey = $oAccount->CryptKey();
				$sPassword = \SnappyMail\Crypt::EncryptToJSON($sRawPassword, $sCryptKey);
				$sPasswordHMAC = \hash_hmac('sha1', $sPassword, $sCryptKey);
			}

			$aCalendarData = [
				'Mode' => 1,
				'User' => $sEmail,
				'Password' => $sPassword,
				'PasswordHMAC' => $sPasswordHMAC,
				'Url' => $sCalDAVUrl
			];

			// Save CalDAV sync data in this plugin's own storage
			$oStorageProvider->Put($oAccount,
				\RainLoop\Providers\Storage\Enumerations\StorageType::CONFIG,
				'calendar_sync',
				\json_encode($aCalendarData)
			);

		} catch (\Exception $e) {
			// Silent fail
		}
	}

	/**
	 * Get additional accounts from storage.
	 */
	private function getAdditionalAccounts(\RainLoop\Model\Account $oAccount, $oStorageProvider)
	{
		try {
			$mData = $oStorageProvider->Get($oAccount,
				\RainLoop\Providers\Storage\Enumerations\StorageType::CONFIG,
				'additionalaccounts'
			);

			if ($mData && \is_string($mData)) {
				$aData = \json_decode($mData, true);
				return \is_array($aData) ? $aData : [];
			}
		} catch (\Exception $e) {
			// Silent fail
		}

		return [];
	}

	/**
	 * Get calendar configuration from this plugin's own calendar_sync storage.
	 *
	 * The config is normally written by AutoConfigureCalDAV() on login, but a
	 * session can be restored without firing 'login.success' (remember me) or
	 * the plugin can be enabled while the user is already logged in. In those
	 * cases the stored config does not exist yet, so build it on demand.
	 */
	private function getCalendarConfig(\RainLoop\Model\Account $oAccount)
	{
		$aConfig = $this->readCalendarConfig($oAccount);
		if (!$aConfig) {
			// Nothing stored for this account yet - configure it now.
			$this->lastConfiguredEmail = null;
			$this->AutoConfigureCalDAV($oAccount);
			$aConfig = $this->readCalendarConfig($oAccount);
		}

		return $aConfig;
	}

	/**
	 * Read the calendar configuration from this plugin's own calendar_sync storage.
	 */
	private function readCalendarConfig(\RainLoop\Model\Account $oAccount)
	{
		try {
			$oStorageProvider = $this->Manager()->Actions()->StorageProvider();
			if (!$oStorageProvider) {
				return null;
			}
			
			// Get this plugin's own calendar_sync config
			$mData = $oStorageProvider->Get($oAccount,
				\RainLoop\Providers\Storage\Enumerations\StorageType::CONFIG,
				'calendar_sync'
			);
			
			if ($mData && \is_string($mData)) {
				$aData = \json_decode($mData, true);
				if (\is_array($aData) && isset($aData['User'], $aData['Password'], $aData['Url'])) {
					
					return [
						'User' => $aData['User'],
						'Password' => $aData['Password'],
						'CalDAVUrl' => rtrim($aData['Url'], '/')
					];
				}
			}
		} catch (\Exception $e) {
			// Silent fail
		}
		
		return null;
	}
	
	/**
	 * Decrypt the stored CalDAV password using the MAIN account's CryptKey.
	 */
	private function getDecryptedPassword(array $aConfig)
	{
		$oMainAccount = $this->Manager()->Actions()->GetMainAccountFromToken();
		if (!$oMainAccount || !method_exists($oMainAccount, 'CryptKey')) {
			return null;
		}
		
		$sPassword = \SnappyMail\Crypt::DecryptFromJSON($aConfig['Password'], $oMainAccount->CryptKey());
		if (is_object($sPassword) && method_exists($sPassword, '__toString')) {
			$sPassword = (string)$sPassword;
		}
		
		return $sPassword;
	}
	
	/**
	 * Build the absolute URL of a single calendar collection.
	 */
	private function calendarUrl(array $aConfig, $sCalendarId) : string
	{
		$sCalendarId = trim((string)$sCalendarId);
		if ('' === $sCalendarId) {
			$sCalendarId = 'default';
		}
		
		return rtrim($aConfig['CalDAVUrl'], '/') . '/' . rawurlencode($sCalendarId);
	}
	
	/**
	 * Resolve a CalDAV href reported by the server into an absolute URL.
	 * If an EventUrl is supplied by the client we must use it unchanged for
	 * the path (it is already URL-encoded by the server); this just adds the
	 * scheme/host when the server returned a server-relative path.
	 */
	private function resolveEventUrl(array $aConfig, $sUrl) : string
	{
		$sUrl = trim((string)$sUrl);
		if ('' === $sUrl) {
			return '';
		}
		if (preg_match('#^https?://#i', $sUrl)) {
			return $sUrl;
		}
		$aParts = parse_url($aConfig['CalDAVUrl']);
		$sScheme = !empty($aParts['scheme']) ? $aParts['scheme'] : 'https';
		if (0 === strpos($sUrl, '//')) {
			// Protocol-relative href
			return $sScheme . ':' . $sUrl;
		}
		if (!empty($aParts['host'])) {
			$sBase = $sScheme . '://' . $aParts['host']
				. (isset($aParts['port']) ? ':' . $aParts['port'] : '');
			return $sBase . '/' . ltrim($sUrl, '/');
		}
		return $sUrl;
	}

	/**
	 * List all calendars available for the account (CalDAV discovery).
	 */
	public function DoGetCalendars() : array
	{
		try {
			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if (!$oAccount) {
				return $this->jsonResponse(__FUNCTION__, ['calendars' => [], 'message' => $this->msg('ERROR_NOT_LOGGED_IN', [], 'Please log in first')]);
			}
			
			$aConfig = $this->getCalendarConfig($oAccount);
			if (!$aConfig) {
				return $this->jsonResponse(__FUNCTION__, ['calendars' => [], 'message' => $this->msg('ERROR_NOT_CONFIGURED_MSG', [], 'Calendar not configured yet. Please check settings.')]);
			}
			
			$sPassword = $this->getDecryptedPassword($aConfig);
			if (null === $sPassword) {
				return $this->jsonResponse(__FUNCTION__, ['calendars' => [], 'error' => $this->msg('ERROR_NO_ENCRYPTION_KEY', [], 'Cannot access encryption key')]);
			}
			
			// PROPFIND the calendar home to discover the collections
			$sHomeUrl = rtrim($aConfig['CalDAVUrl'], '/') . '/';
			$sBody = '<?xml version="1.0" encoding="utf-8" ?>' . "\n";
			$sBody .= '<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/">' . "\n";
			$sBody .= '  <D:prop>' . "\n";
			$sBody .= '    <D:displayname />' . "\n";
			$sBody .= '    <D:resourcetype />' . "\n";
			$sBody .= '    <A:calendar-color />' . "\n";
			$sBody .= '  </D:prop>' . "\n";
			$sBody .= '</D:propfind>';
			
			$result = $this->makeCalDAVRequest(
				$sHomeUrl,
				'PROPFIND',
				$aConfig['User'],
				$sPassword,
				$sBody,
				[
					'Content-Type: application/xml; charset=utf-8',
					'Depth: 1'
				]
			);
			
			$aCalendars = [];
			if ($result['code'] === 207) {
				$aCalendars = $this->parseCalendarsResponse($result['body']);
			}
			
			// Always provide at least one usable calendar so the UI keeps working
			if (!$aCalendars) {
				$aCalendars = [
					['id' => 'default', 'name' => $this->msg('CALENDAR', [], 'Calendar'), 'color' => '#00639a']
				];
			}
			
			return $this->jsonResponse(__FUNCTION__, ['calendars' => $aCalendars]);
			
		} catch (\Exception $e) {
			return $this->jsonResponse(__FUNCTION__, ['calendars' => [], 'error' => $e->getMessage()]);
		}
	}
	
	/**
	 * Parse a CalDAV PROPFIND multistatus response into a list of calendars.
	 */
	private function parseCalendarsResponse($xml) : array
	{
		$aCalendars = [];
		
		try {
			$doc = new \DOMDocument();
			$doc->loadXML($xml);
			
			$xpath = new \DOMXPath($doc);
			$xpath->registerNamespace('D', 'DAV:');
			$xpath->registerNamespace('C', 'urn:ietf:params:xml:ns:caldav');
			$xpath->registerNamespace('A', 'http://apple.com/ns/ical/');
			
			$aPalette = ['#00639a', '#16a765', '#e67c73', '#f6bf26', '#8e24aa', '#f4511e', '#039be5', '#7cb342'];
			$i = 0;
			
			foreach ($xpath->query('//D:response') as $response) {
				// Only keep calendar collections
				if (0 === $xpath->query('.//D:resourcetype/C:calendar', $response)->length) {
					continue;
				}
				
				$hrefNodes = $xpath->query('./D:href', $response);
				if (!$hrefNodes->length) {
					continue;
				}
				
				$sHref = rtrim(trim($hrefNodes->item(0)->nodeValue), '/');
				if ('' === $sHref) {
					continue;
				}
				
				$aSegments = explode('/', $sHref);
				$sId = rawurldecode(end($aSegments));
				if ('' === $sId) {
					continue;
				}
				
				$sName = '';
				$nameNodes = $xpath->query('.//D:displayname', $response);
				if ($nameNodes->length) {
					$sName = trim($nameNodes->item(0)->nodeValue);
				}
				if ('' === $sName) {
					$sName = $sId;
				}
				
				$sColor = '';
				$colorNodes = $xpath->query('.//A:calendar-color', $response);
				if ($colorNodes->length) {
					$sColor = trim($colorNodes->item(0)->nodeValue);
				}
				// Normalize #RRGGBBAA to #RRGGBB
				if (preg_match('/^#([0-9a-fA-F]{6})/', $sColor, $m)) {
					$sColor = '#' . $m[1];
				} else {
					$sColor = $aPalette[$i % count($aPalette)];
				}
				
				$aCalendars[] = [
					'id' => $sId,
					'name' => $sName,
					'color' => $sColor
				];
				++$i;
			}
		} catch (\Exception $e) {
			// Silent fail, caller provides a fallback calendar
		}
		
		return $aCalendars;
	}
	
	/**
	 * Make CalDAV request
	 */
	private function makeCalDAVRequest($url, $method, $username, $password, $body = null, $headers = [])
	{
		$ch = curl_init($url);
		
		curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
		curl_setopt($ch, CURLOPT_CUSTOMREQUEST, $method);
		curl_setopt($ch, CURLOPT_HTTPAUTH, CURLAUTH_BASIC);
		curl_setopt($ch, CURLOPT_USERPWD, "{$username}:{$password}");
		curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, true);
		
		$defaultHeaders = [
			'Content-Type: application/xml; charset=utf-8',
			'Depth: 1'
		];
		
		$allHeaders = array_merge($defaultHeaders, $headers);
		curl_setopt($ch, CURLOPT_HTTPHEADER, $allHeaders);
		
		if ($body) {
			curl_setopt($ch, CURLOPT_POSTFIELDS, $body);
		}
		
		$response = curl_exec($ch);
		$httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
		$error = curl_error($ch);
		
		curl_close($ch);
		
		return [
			'code' => $httpCode,
			'body' => $response,
			'error' => $error
		];
	}
	
	/**
	 * Make JMAP request
	 */
	private function makeJMAPRequest($url, $username, $password, $data)
	{
		$ch = curl_init($url);
		
		curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
		curl_setopt($ch, CURLOPT_POST, true);
		curl_setopt($ch, CURLOPT_HTTPAUTH, CURLAUTH_BASIC);
		curl_setopt($ch, CURLOPT_USERPWD, "{$username}:{$password}");
		curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, true);
		curl_setopt($ch, CURLOPT_HTTPHEADER, [
			'Content-Type: application/json',
			'Accept: application/json'
		]);
		curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($data));
		
		$response = curl_exec($ch);
		$httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
		$error = curl_error($ch);
		
		curl_close($ch);
		
		return [
			'code' => $httpCode,
			'body' => $response ? json_decode($response, true) : null,
			'error' => $error
		];
	}
	
	/**
	 * Parse iCalendar data into a list of events.
	 *
	 * Master VEVENTs and their per-occurrence overrides (VEVENTs carrying a
	 * RECURRENCE-ID) are both returned. Recurrence data (RRULE/RDATE/EXDATE) is
	 * preserved so the client can expand the series into occurrences.
	 */
	private function parseICalendar($icalData, $sHref = '', $sEtag = '')
	{
		$events = [];
		$currentEvent = null;
		// Properties that may legally occur more than once in a VEVENT
		$aMulti = ['rdate', 'exdate', 'categories'];

		foreach ($this->unfoldIcs($icalData) as $line) {
			$line = rtrim($line);
			if ('' === $line) {
				continue;
			}

			if (0 === strcasecmp($line, 'BEGIN:VEVENT')) {
				$currentEvent = [];
				continue;
			}
			if (0 === strcasecmp($line, 'END:VEVENT') && null !== $currentEvent) {
				$sRecurrenceRaw = (string)($currentEvent['recurrence-id'] ?? '');
				$events[] = [
					'uid' => $currentEvent['uid'] ?? '',
					'summary' => $this->unescapeICSText((string) ($currentEvent['summary'] ?? 'Untitled')),
					'dtstart' => $this->parseICalDate($currentEvent['dtstart'] ?? '', $currentEvent['dtstart_tzid'] ?? ''),
					'dtend' => $this->parseICalDate($currentEvent['dtend'] ?? '', $currentEvent['dtend_tzid'] ?? ''),
					'description' => $this->unescapeICSText((string) ($currentEvent['description'] ?? '')),
					'location' => $this->unescapeICSText((string) ($currentEvent['location'] ?? '')),
					'allDay' => !isset($currentEvent['dtstart']) || false === strpos((string)$currentEvent['dtstart'], 'T'),
					'href' => $sHref,
					'etag' => $sEtag,
					'rrule' => (string)($currentEvent['rrule'] ?? ''),
					'rdate' => $currentEvent['rdate'] ?? [],
					'exdate' => $currentEvent['exdate'] ?? [],
					'recurrenceId' => '' === $sRecurrenceRaw ? '' : $this->parseICalDate($sRecurrenceRaw, $currentEvent['recurrence-id_tzid'] ?? ''),
					'sequence' => (string)($currentEvent['sequence'] ?? '0'),
					'status' => (string)($currentEvent['status'] ?? ''),
					'organizer' => (string)($currentEvent['organizer'] ?? '')
				];
				$currentEvent = null;
				continue;
			}
			if (null === $currentEvent) {
				continue;
			}

			$iPos = strpos($line, ':');
			if (false === $iPos) {
				continue;
			}
			$sHead = substr($line, 0, $iPos);
			$sValue = substr($line, $iPos + 1);
			$sKey = strtolower(strtok($sHead, ';'));
			$sTzid = '';
			if (preg_match('/;TZID=([^;:]+)/i', $sHead, $tm)) {
				$sTzid = trim($tm[1], '"');
			}

			if (in_array($sKey, $aMulti, true)) {
				if (!isset($currentEvent[$sKey]) || !is_array($currentEvent[$sKey])) {
					$currentEvent[$sKey] = [];
				}
				foreach (explode(',', $sValue) as $sItem) {
					$sItem = trim($sItem);
					if ('' !== $sItem) {
						$currentEvent[$sKey][] = $this->parseICalDate($sItem, $sTzid);
					}
				}
			} else {
				$currentEvent[$sKey] = $sValue;
				if ('' !== $sTzid) {
					$currentEvent[$sKey . '_tzid'] = $sTzid;
				}
			}
		}

		return $events;
	}
	
	/**
	 * Parse iCalendar date format to ISO string.
	 *
	 * A floating local time that carries a TZID (e.g. DTSTART;TZID=Europe/Berlin)
	 * is converted to UTC so the instant is preserved regardless of the viewer's
	 * timezone.
	 */
	private function parseICalDate($dateStr, $sTzid = '')
	{
		$dateStr = trim((string)$dateStr);
		if ('' === $dateStr) {
			return '';
		}
		
		// Handle YYYYMMDD format
		if (preg_match('/^(\d{4})(\d{2})(\d{2})$/', $dateStr, $matches)) {
			return $matches[1] . '-' . $matches[2] . '-' . $matches[3];
		}
		
		// Handle YYYYMMDDTHHmmssZ format (already UTC)
		if (preg_match('/^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z$/', $dateStr, $matches)) {
			return $matches[1] . '-' . $matches[2] . '-' . $matches[3] . 'T' . 
			       $matches[4] . ':' . $matches[5] . ':' . $matches[6] . 'Z';
		}
		
		// Handle YYYYMMDDTHHmmss format (floating or with a TZID)
		if (preg_match('/^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})$/', $dateStr, $matches)) {
			if ('' !== $sTzid) {
				$sUtc = $this->tzidToUtcBasic($dateStr, $sTzid);
				if (null !== $sUtc) {
					return $this->parseICalDate($sUtc);
				}
			}
			// Unknown/absent zone: preserve previous behaviour (treat as UTC)
			return $matches[1] . '-' . $matches[2] . '-' . $matches[3] . 'T' . 
			       $matches[4] . ':' . $matches[5] . ':' . $matches[6] . 'Z';
		}
		
		return $dateStr;
	}

	/**
	 * Convert a TZID wall-clock time (YYYYMMDDTHHmmss) into a UTC value.
	 * Returns null when the timezone is unknown.
	 */
	private function tzidToUtcBasic(string $sBasic, string $sTzid) : ?string
	{
		if (!preg_match('/^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})$/', $sBasic, $m)) {
			return null;
		}
		$sTzid = \trim($sTzid, '"');
		if ('' === $sTzid || 0 === \strcasecmp($sTzid, 'UTC')) {
			return $sBasic . 'Z';
		}

		// Try the TZID as-is, then a few common variants (leading slash and
		// global "/vendor/.../Region/City" identifiers).
		$aCandidates = [$sTzid];
		if (false !== \strpos($sTzid, '/')) {
			$aCandidates[] = \ltrim($sTzid, '/');
			$aCandidates[] = \substr($sTzid, \strrpos($sTzid, '/') + 1);
		}

		$sDateTime = $m[1] . '-' . $m[2] . '-' . $m[3] . ' ' . $m[4] . ':' . $m[5] . ':' . $m[6];
		foreach ($aCandidates as $sCandidate) {
			try {
				$dt = new \DateTime($sDateTime, new \DateTimeZone($sCandidate));
				$dt->setTimezone(new \DateTimeZone('UTC'));
				return $dt->format('Ymd\THis\Z');
			} catch (\Exception $e) {
				// Try the next candidate
			}
		}
		return null;
	}
	
	/**
	 * Get calendar events
	 */
	public function DoGetCalendarEvents() : array
	{
		try {
			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if (!$oAccount) {
				return $this->jsonResponse(__FUNCTION__, ['events' => [], 'message' => $this->msg('ERROR_NOT_LOGGED_IN', [], 'Please log in first')]);
			}
			
			// Get config from this plugin's own calendar_sync storage
			$aConfig = $this->getCalendarConfig($oAccount);
			if (!$aConfig) {
				return $this->jsonResponse(__FUNCTION__, ['events' => [], 'message' => $this->msg('ERROR_NOT_CONFIGURED_MSG', [], 'Calendar not configured yet. Please check settings.')]);
			}
			
			// Decrypt password using MAIN account's CryptKey
			$oMainAccount = $this->Manager()->Actions()->GetMainAccountFromToken();
			if (!$oMainAccount || !method_exists($oMainAccount, 'CryptKey')) {
				return $this->jsonResponse(__FUNCTION__, ['events' => [], 'error' => $this->msg('ERROR_NO_ENCRYPTION_KEY', [], 'Cannot access encryption key')]);
			}
			
			$sCryptKey = $oMainAccount->CryptKey();
			$sPassword = \SnappyMail\Crypt::DecryptFromJSON($aConfig['Password'], $sCryptKey);
			
			if (is_object($sPassword) && method_exists($sPassword, '__toString')) {
				$sPassword = (string)$sPassword;
			}
			
			// Build CalDAV URL for the requested calendar collection
			$sCalDAVUrl = $this->calendarUrl($aConfig, $this->jsonParam('CalendarId', 'default')) . '/';
			
			// CalDAV REPORT query for events
			$sReportBody = '<?xml version="1.0" encoding="utf-8" ?>' . "\n";
			$sReportBody .= '<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">' . "\n";
			$sReportBody .= '  <D:prop>' . "\n";
			$sReportBody .= '    <D:getetag />' . "\n";
			$sReportBody .= '    <C:calendar-data />' . "\n";
			$sReportBody .= '  </D:prop>' . "\n";
			$sReportBody .= '  <C:filter>' . "\n";
			$sReportBody .= '    <C:comp-filter name="VCALENDAR">' . "\n";
			$sReportBody .= '      <C:comp-filter name="VEVENT" />' . "\n";
			$sReportBody .= '    </C:comp-filter>' . "\n";
			$sReportBody .= '  </C:filter>' . "\n";
			$sReportBody .= '</C:calendar-query>';
			
			$result = $this->makeCalDAVRequest(
				$sCalDAVUrl,
				'REPORT',
				$aConfig['User'],
				$sPassword,
				$sReportBody,
				[
					'Content-Type: application/xml; charset=utf-8',
					'Depth: 1'
				]
			);
			
			
			$aEvents = [];
			if ($result['code'] === 207) {
				// Parse multistatus response
				$aEvents = $this->parseCalDAVResponse($result['body']);
			} else {
			}
			
			return $this->jsonResponse(__FUNCTION__, ['events' => $aEvents, 'message' => $this->msg('LOADED_EVENTS', ['COUNT' => count($aEvents)], 'Loaded ' . count($aEvents) . ' events')]);
			
		} catch (\Exception $e) {
			return $this->jsonResponse(__FUNCTION__, ['events' => [], 'error' => $e->getMessage()]);
		}
	}
	
	/**
	 * Create calendar event
	 */
	public function DoCreateCalendarEvent() : array
	{
		try {
			
			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if (!$oAccount) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_LOGGED_IN', [], 'Not logged in')]);
			}
			
			
			// Get config from this plugin's own calendar_sync storage
			$aConfig = $this->getCalendarConfig($oAccount);
			if (!$aConfig) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_CONFIGURED', [], 'Calendar not configured')]);
			}
			
			
			// Decrypt password using MAIN account's CryptKey
			$oMainAccount = $this->Manager()->Actions()->GetMainAccountFromToken();
			
			if (!$oMainAccount || !method_exists($oMainAccount, 'CryptKey')) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NO_ENCRYPTION_KEY', [], 'Cannot access encryption key')]);
			}
			
			$sCryptKey = $oMainAccount->CryptKey();
			$sPassword = \SnappyMail\Crypt::DecryptFromJSON($aConfig['Password'], $sCryptKey);
			
			if (is_object($sPassword) && method_exists($sPassword, '__toString')) {
				$sPassword = (string)$sPassword;
			}
			
			
			// Get event data from request (JS sends Title, Start, End, etc.)
			$sCalendarId = $this->jsonParam('CalendarId', 'default');
			$sTitle = $this->jsonParam('Title', '');
			$sStart = $this->jsonParam('Start', '');
			$sEnd = $this->jsonParam('End', '');
			$bAllDay = $this->jsonParam('AllDay', false);
			$sDescription = $this->jsonParam('Description', '');
			$sLocation = $this->jsonParam('Location', '');
			$sRrule = \trim((string) $this->jsonParam('Rrule', ''));
			$sExdate = (string) $this->jsonParam('Exdate', '');
			$sRdate = (string) $this->jsonParam('Rdate', '');
			
			
			if (empty($sTitle)) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_TITLE_REQUIRED', [], 'Event title required')]);
			}
			
			// Generate UID for event
			$sUid = \uniqid('event-') . '@' . $aConfig['User'];
			
			// Format dates
			// For all-day events, JS sends date-only string like "2025-11-06"
			// For timed events, JS sends ISO string with time like "2025-11-06T14:30:00.000Z"
			if ($bAllDay) {
				// Date-only string, just remove dashes: 2025-11-06 -> 20251106
				// For all-day events, use the date as-is without timezone conversion
				// This prevents the "day before" issue when user is in timezone ahead of UTC
				$sStartFormatted = str_replace('-', '', $sStart);
				$sEndFormatted = str_replace('-', '', $sEnd);
			} else {
				// For timed events, parse the ISO string and convert to UTC
				// The ISO string from JS is already in UTC (ends with Z)
				$dtStart = new \DateTime($sStart, new \DateTimeZone('UTC'));
				$dtEnd = new \DateTime($sEnd, new \DateTimeZone('UTC'));
				$sStartFormatted = $dtStart->format('Ymd\THis\Z');
				$sEndFormatted = $dtEnd->format('Ymd\THis\Z');
			}
			
			// Create iCalendar format
			$sICS = "BEGIN:VCALENDAR\r\n";
			$sICS .= "VERSION:2.0\r\n";
			$sICS .= "PRODID:-//Mailbux//CalDAV Plugin//EN\r\n";
			$sICS .= "BEGIN:VEVENT\r\n";
			$sICS .= "UID:" . $sUid . "\r\n";
			$sICS .= "DTSTAMP:" . gmdate('Ymd\THis\Z') . "\r\n";
			$sICS .= "DTSTART" . ($bAllDay ? ';VALUE=DATE' : '') . ":" . $sStartFormatted . "\r\n";
			$sICS .= "DTEND" . ($bAllDay ? ';VALUE=DATE' : '') . ":" . $sEndFormatted . "\r\n";
			$sICS .= "SUMMARY:" . $this->escapeICS($sTitle) . "\r\n";
			
			if (!empty($sDescription)) {
				$sICS .= "DESCRIPTION:" . $this->escapeICS($sDescription) . "\r\n";
			}
			if (!empty($sLocation)) {
				$sICS .= "LOCATION:" . $this->escapeICS($sLocation) . "\r\n";
			}
			if ('' !== $sRrule) {
				$sICS .= "RRULE:" . $sRrule . "\r\n";
			}
			$sICS .= $this->buildDatePropertyLines('RDATE', $sRdate, $bAllDay);
			$sICS .= $this->buildDatePropertyLines('EXDATE', $sExdate, $bAllDay);
			
			$sICS .= "END:VEVENT\r\n";
			$sICS .= "END:VCALENDAR\r\n";
			
			// PUT event to CalDAV server
			$sEventUrl = $this->calendarUrl($aConfig, $sCalendarId) . '/' . rawurlencode($sUid) . '.ics';
			
			
			$result = $this->makeCalDAVRequest(
				$sEventUrl,
				'PUT',
				$aConfig['User'],
				$sPassword,
				$sICS,
				['Content-Type: text/calendar; charset=utf-8']
			);
			
			
			if ($result['code'] === 201 || $result['code'] === 204) {
				return $this->jsonResponse(__FUNCTION__, ['success' => true, 'uid' => $sUid]);
			} else {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_CALDAV', ['CODE' => $result['code']], 'CalDAV error: ' . $result['code'])]);
			}
			
		} catch (\Exception $e) {
			return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $e->getMessage()]);
		}
	}
	
	/**
	 * Update calendar event
	 */
	public function DoUpdateCalendarEvent() : array
	{
		try {
			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if (!$oAccount) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_LOGGED_IN', [], 'Not logged in')]);
			}
			
			$aConfig = $this->getCalendarConfig($oAccount);
			if (!$aConfig) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_CONFIGURED', [], 'Calendar not configured')]);
			}
			
			$sEventId = $this->jsonParam('EventId', '');
			$sCalendarId = $this->jsonParam('CalendarId', 'default');
			$sTitle = $this->jsonParam('Title', '');
			$sStart = $this->jsonParam('Start', '');
			$sEnd = $this->jsonParam('End', '');
			$bAllDay = $this->jsonParam('AllDay', false);
			$sDescription = $this->jsonParam('Description', '');
			$sLocation = $this->jsonParam('Location', '');
			$sEventUrlParam = $this->jsonParam('EventUrl', '');
			$sRrule = \trim((string) $this->jsonParam('Rrule', ''));
			$sExdate = (string) $this->jsonParam('Exdate', '');
			$sRdate = (string) $this->jsonParam('Rdate', '');
			$sMode = \strtolower(\trim((string) $this->jsonParam('Mode', '')));
			$sRecurrenceId = \trim((string) $this->jsonParam('RecurrenceId', ''));
			
			if (!$sEventId || !$sTitle) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_EVENT_ID_TITLE_REQUIRED', [], 'Event ID and title required')]);
			}
			
			// Format dates
			if ($bAllDay) {
				// For all-day events, use date string directly without timezone conversion
				// JS sends date-only string like "2025-11-06" or ISO string
				// Extract date part if it's an ISO string
				$sStartDate = preg_match('/^(\d{4}-\d{2}-\d{2})/', $sStart, $mStart) ? $mStart[1] : $sStart;
				$sEndDate = preg_match('/^(\d{4}-\d{2}-\d{2})/', $sEnd, $mEnd) ? $mEnd[1] : $sEnd;
				$sStartFormatted = str_replace('-', '', $sStartDate);
				$sEndFormatted = str_replace('-', '', $sEndDate);
			} else {
				// For timed events, parse ISO string and convert to UTC
				$dtStart = new \DateTime($sStart, new \DateTimeZone('UTC'));
				$dtEnd = new \DateTime($sEnd, new \DateTimeZone('UTC'));
				$sStartFormatted = $dtStart->format('Ymd\THis\Z');
				$sEndFormatted = $dtEnd->format('Ymd\THis\Z');
			}
			
			$aFields = [
				'summary' => $sTitle,
				'start' => $sStartFormatted,
				'end' => $sEndFormatted,
				'allDay' => (bool) $bAllDay,
				'description' => $sDescription,
				'location' => $sLocation,
				'rrule' => $sRrule,
				'exdate' => $sExdate,
				'rdate' => $sRdate
			];
			
			// Decrypt password using MAIN account's CryptKey
			$oMainAccount = $this->Manager()->Actions()->GetMainAccountFromToken();
			if (!$oMainAccount || !method_exists($oMainAccount, 'CryptKey')) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NO_ENCRYPTION_KEY', [], 'Cannot access encryption key')]);
			}
			
			$sCryptKey = $oMainAccount->CryptKey();
			$sPassword = \SnappyMail\Crypt::DecryptFromJSON($aConfig['Password'], $sCryptKey);
			
			if (is_object($sPassword) && method_exists($sPassword, '__toString')) {
				$sPassword = (string)$sPassword;
			}
			
			// Reuse the server-reported resource URL when available
			$sEventUrl = $this->resolveEventUrl($aConfig, $sEventUrlParam);
			if ('' === $sEventUrl) {
				$sEventUrl = $this->calendarUrl($aConfig, $sCalendarId) . '/' . rawurlencode($sEventId) . '.ics';
			}
			
			// Fetch the current resource so recurrence rules, overrides and any
			// properties the plugin does not understand survive the update.
			$getResult = $this->makeCalDAVRequest($sEventUrl, 'GET', $aConfig['User'], $sPassword);
			$sExisting = (200 === $getResult['code'] && !empty($getResult['body'])) ? (string) $getResult['body'] : '';
			
			$bOccurrence = ('occurrence' === $sMode) || ('' !== $sRecurrenceId);
			
			if ($bOccurrence) {
				// Edit a single occurrence of a recurring series (RECURRENCE-ID override)
				$sICS = $this->buildOccurrenceOverrideIcs($sExisting, $sEventId, $sRecurrenceId, $aFields);
			} elseif ('' !== $sExisting && false !== \stripos($sExisting, 'BEGIN:VEVENT')) {
				// Edit the whole series in place (keeps RRULE/EXDATE and overrides)
				$sICS = $this->updateSeriesIcs($sExisting, $aFields);
			} else {
				$sICS = $this->buildNewEventIcs($sEventId, $aFields);
			}
			
			$result = $this->makeCalDAVRequest(
				$sEventUrl,
				'PUT',
				$aConfig['User'],
				$sPassword,
				$sICS,
				['Content-Type: text/calendar; charset=utf-8']
			);
			
			if ($result['code'] === 201 || $result['code'] === 204) {
				return $this->jsonResponse(__FUNCTION__, ['success' => true]);
			} else {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_CALDAV', ['CODE' => $result['code']], 'CalDAV error: ' . $result['code'])]);
			}
			
		} catch (\Exception $e) {
			return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $e->getMessage()]);
		}
	}
	
	/**
	 * Delete calendar event
	 */
	public function DoDeleteCalendarEvent() : array
	{
		try {
			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if (!$oAccount) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_LOGGED_IN', [], 'Not logged in')]);
			}
			
			$aConfig = $this->getCalendarConfig($oAccount);
			if (!$aConfig) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_CONFIGURED', [], 'Calendar not configured')]);
			}
			
			$sEventId = $this->jsonParam('EventId', '');
			$sCalendarId = $this->jsonParam('CalendarId', 'default');
			$sEventUrlParam = $this->jsonParam('EventUrl', '');
			if (!$sEventId) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_EVENT_ID_REQUIRED', [], 'Event ID required')]);
			}
			
			// Decrypt password using MAIN account's CryptKey
			$oMainAccount = $this->Manager()->Actions()->GetMainAccountFromToken();
			if (!$oMainAccount || !method_exists($oMainAccount, 'CryptKey')) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NO_ENCRYPTION_KEY', [], 'Cannot access encryption key')]);
			}
			
			$sCryptKey = $oMainAccount->CryptKey();
			$sPassword = \SnappyMail\Crypt::DecryptFromJSON($aConfig['Password'], $sCryptKey);
			
			if (is_object($sPassword) && method_exists($sPassword, '__toString')) {
				$sPassword = (string)$sPassword;
			}
			
			// DELETE event from CalDAV server
			// Prefer the exact server-reported resource URL, else derive it from the UID
			$sEventUrl = $this->resolveEventUrl($aConfig, $sEventUrlParam);
			if ('' === $sEventUrl) {
				$sEventUrl = $this->calendarUrl($aConfig, $sCalendarId) . '/' . rawurlencode($sEventId) . '.ics';
			}
			
			
			$result = $this->makeCalDAVRequest(
				$sEventUrl,
				'DELETE',
				$aConfig['User'],
				$sPassword
			);
			
			
			if ($result['code'] === 204 || $result['code'] === 200) {
				return $this->jsonResponse(__FUNCTION__, ['success' => true]);
			} else {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_CALDAV', ['CODE' => $result['code']], 'CalDAV error: ' . $result['code'])]);
			}
			
		} catch (\Exception $e) {
			return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $e->getMessage()]);
		}
	}
	
	/**
	 * Escape text for iCalendar format
	 */
	private function escapeICS($text)
	{
		return str_replace(["\r\n", "\n", "\r", ",", ";"], ["\\n", "\\n", "\\n", "\\,", "\\;"], $text);
	}

	/* ------------------------------------------------------------------
	   Recurrence helpers
	   ------------------------------------------------------------------ */

	/**
	 * Build a standalone VCALENDAR/VEVENT resource for a single event.
	 */
	private function buildNewEventIcs(string $sUid, array $aFields) : string
	{
		$aIcs = [
			'BEGIN:VCALENDAR',
			'VERSION:2.0',
			'PRODID:-//Mailbux//CalDAV Plugin//EN',
			'BEGIN:VEVENT',
			'UID:' . $sUid,
			'DTSTAMP:' . \gmdate('Ymd\THis\Z')
		];
		foreach ($this->buildVEventDetailLines($aFields) as $sLine) {
			$aIcs[] = $sLine;
		}
		$aIcs[] = 'END:VEVENT';
		$aIcs[] = 'END:VCALENDAR';
		return \implode("\r\n", $aIcs) . "\r\n";
	}

	/**
	 * DTSTART/DTEND/SUMMARY/DESCRIPTION/LOCATION/(RRULE|RDATE|EXDATE) lines.
	 */
	private function buildVEventDetailLines(array $aFields) : array
	{
		$bAllDay = !empty($aFields['allDay']);
		$aOut = [];
		$aOut[] = 'DTSTART' . ($bAllDay ? ';VALUE=DATE' : '') . ':' . $aFields['start'];
		$aOut[] = 'DTEND' . ($bAllDay ? ';VALUE=DATE' : '') . ':' . $aFields['end'];
		$aOut[] = 'SUMMARY:' . $this->escapeICSText((string) $aFields['summary']);
		if ('' !== \trim((string) $aFields['description'])) {
			$aOut[] = 'DESCRIPTION:' . $this->escapeICSText((string) $aFields['description']);
		}
		if ('' !== \trim((string) $aFields['location'])) {
			$aOut[] = 'LOCATION:' . $this->escapeICSText((string) $aFields['location']);
		}
		if (isset($aFields['rrule']) && '' !== \trim((string) $aFields['rrule'])) {
			$aOut[] = 'RRULE:' . \trim((string) $aFields['rrule']);
		}
		foreach ($this->buildDatePropertyLinesArr('RDATE', (string) ($aFields['rdate'] ?? ''), $bAllDay) as $sLine) {
			$aOut[] = $sLine;
		}
		foreach ($this->buildDatePropertyLinesArr('EXDATE', (string) ($aFields['exdate'] ?? ''), $bAllDay) as $sLine) {
			$aOut[] = $sLine;
		}
		return $aOut;
	}

	/**
	 * RDATE/EXDATE lines (string form, ready to concatenate into an ICS blob).
	 */
	private function buildDatePropertyLines(string $sName, string $sValues, bool $bAllDay) : string
	{
		$a = $this->buildDatePropertyLinesArr($sName, $sValues, $bAllDay);
		return $a ? \implode("\r\n", $a) . "\r\n" : '';
	}

	/**
	 * RDATE/EXDATE lines (array form) from a client supplied value list.
	 * Accepts comma and/or newline separated values in ISO or iCalendar form.
	 */
	private function buildDatePropertyLinesArr(string $sName, string $sValues, bool $bAllDay) : array
	{
		$aOut = [];
		$sValues = \trim($sValues);
		if ('' === $sValues) {
			return $aOut;
		}
		foreach (\preg_split('/[\r\n]+/', $sValues) as $sLine) {
			foreach (\explode(',', $sLine) as $sVal) {
				$sFmt = $this->formatIcsDateValue($sVal, $bAllDay);
				if ('' !== $sFmt) {
					$aOut[] = $sName . ($bAllDay ? ';VALUE=DATE' : '') . ':' . $sFmt;
				}
			}
		}
		return $aOut;
	}

	/**
	 * Normalise a client supplied date value (ISO or iCalendar basic) to an
	 * iCalendar property value. Timed values are converted to UTC.
	 */
	private function formatIcsDateValue($sValue, bool $bAllDay) : string
	{
		$sValue = \trim((string) $sValue);
		if ('' === $sValue) {
			return '';
		}
		if ($bAllDay) {
			if (\preg_match('/^(\d{4})(\d{2})(\d{2})/', $sValue, $m)) {
				return $m[1] . $m[2] . $m[3];
			}
			return \str_replace('-', '', \substr($sValue, 0, 10));
		}
		if (\preg_match('/^\d{8}T\d{6}Z?$/', $sValue)) {
			return $sValue;
		}
		try {
			$dt = new \DateTime($sValue);
			$dt->setTimezone(new \DateTimeZone('UTC'));
			return $dt->format('Ymd\THis\Z');
		} catch (\Exception $e) {
			return $sValue;
		}
	}

	/**
	 * Rewrite a stored VCALENDAR so its master VEVENT matches $aFields while
	 * keeping recurrence overrides and unrelated properties intact.
	 */
	private function updateSeriesIcs(string $sIcs, array $aFields) : string
	{
		$aLines = $this->unfoldIcs($sIcs);
		$iCount = \count($aLines);
		$aManaged = ['DTSTART', 'DTEND', 'SUMMARY', 'DESCRIPTION', 'LOCATION', 'RRULE', 'EXDATE'];
		$aOut = [];
		$bMasterDone = false;

		for ($i = 0; $i < $iCount; $i++) {
			$sLine = \rtrim($aLines[$i]);
			if ('' === $sLine) {
				continue;
			}
			if (0 === \strcasecmp($sLine, 'BEGIN:VEVENT')) {
				// Locate this block
				$bOverride = false;
				$iEnd = $i;
				for ($j = $i + 1; $j < $iCount; $j++) {
					$sInner = \rtrim($aLines[$j]);
					if (0 === \strcasecmp($sInner, 'END:VEVENT')) {
						$iEnd = $j;
						break;
					}
					if (0 === \stripos(\ltrim($sInner), 'RECURRENCE-ID')) {
						$bOverride = true;
					}
				}
				if (!$bOverride && !$bMasterDone) {
					$bMasterDone = true;
					$aOut[] = 'BEGIN:VEVENT';
					for ($j = $i + 1; $j <= $iEnd; $j++) {
						$sInner = \rtrim($aLines[$j]);
						if (0 === \strcasecmp($sInner, 'END:VEVENT')) {
							foreach ($this->buildVEventDetailLines($aFields) as $sNew) {
								$aOut[] = $sNew;
							}
							$aOut[] = 'END:VEVENT';
							break;
						}
						$sKey = \strtoupper(\strtok(\substr($sInner, 0, false !== \strpos($sInner, ':') ? \strpos($sInner, ':') : \strlen($sInner)), ';'));
						if (\in_array($sKey, $aManaged, true)) {
							continue;
						}
						$aOut[] = $sInner;
					}
					$i = $iEnd;
					continue;
				}
				// Non-master (override) block: copy verbatim
				for ($j = $i; $j <= $iEnd; $j++) {
					$aOut[] = \rtrim($aLines[$j]);
				}
				$i = $iEnd;
				continue;
			}
			$aOut[] = $sLine;
		}

		return \implode("\r\n", $aOut) . "\r\n";
	}

	/**
	 * Add/replace a single occurrence (RECURRENCE-ID override) inside a stored
	 * VCALENDAR, preserving the master and any other overrides.
	 */
	private function buildOccurrenceOverrideIcs(string $sExisting, string $sUid, string $sRecurrenceId, array $aFields) : string
	{
		$aFields['rrule'] = '';
		$aFields['exdate'] = '';
		$aFields['rdate'] = '';

		$sRecFmt = $this->formatIcsDateValue($sRecurrenceId, !empty($aFields['allDay']));

		$aBlock = ['BEGIN:VEVENT', 'UID:' . $sUid];
		if ('' !== $sRecFmt) {
			$aBlock[] = 'RECURRENCE-ID' . (!empty($aFields['allDay']) ? ';VALUE=DATE' : '') . ':' . $sRecFmt;
		}
		$aBlock[] = 'DTSTAMP:' . \gmdate('Ymd\THis\Z');
		foreach ($this->buildVEventDetailLines($aFields) as $sLine) {
			$aBlock[] = $sLine;
		}
		$aBlock[] = 'END:VEVENT';

		if ('' === $sExisting || false === \stripos($sExisting, 'BEGIN:VEVENT')) {
			return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Mailbux//CalDAV Plugin//EN\r\n"
				. \implode("\r\n", $aBlock) . "\r\nEND:VCALENDAR\r\n";
		}

		$sTarget = $this->parseICalDate($sRecurrenceId);
		$aLines = $this->unfoldIcs($sExisting);
		$iCount = \count($aLines);
		$aOut = [];
		$bInserted = false;

		for ($i = 0; $i < $iCount; $i++) {
			$sLine = \rtrim($aLines[$i]);
			if ('' === $sLine) {
				continue;
			}
			if (0 === \strcasecmp($sLine, 'BEGIN:VEVENT')) {
				$iEnd = $i;
				for ($j = $i + 1; $j < $iCount; $j++) {
					if (0 === \strcasecmp(\rtrim($aLines[$j]), 'END:VEVENT')) {
						$iEnd = $j;
						break;
					}
				}
				$bSame = false;
				for ($j = $i + 1; $j < $iEnd; $j++) {
					$sInner = \ltrim($aLines[$j]);
					if (0 === \stripos($sInner, 'RECURRENCE-ID') && false !== \strpos($sInner, ':')) {
						$sRaw = \trim(\substr($sInner, \strpos($sInner, ':') + 1));
						if ($this->parseICalDate($sRaw) === $sTarget) {
							$bSame = true;
						}
						break;
					}
				}
				if ($bSame) {
					foreach ($aBlock as $sNew) {
						$aOut[] = $sNew;
					}
					$bInserted = true;
				} else {
					for ($j = $i; $j <= $iEnd; $j++) {
						$aOut[] = \rtrim($aLines[$j]);
					}
				}
				$i = $iEnd;
				continue;
			}
			if (0 === \strcasecmp($sLine, 'END:VCALENDAR') && !$bInserted) {
				foreach ($aBlock as $sNew) {
					$aOut[] = $sNew;
				}
				$bInserted = true;
				$aOut[] = $sLine;
				continue;
			}
			$aOut[] = $sLine;
		}

		return \implode("\r\n", $aOut) . "\r\n";
	}

	/**
	 * Add an EXDATE for one occurrence and drop any override for that occurrence.
	 * Used when deleting a single occurrence of a recurring event.
	 */
	private function addExdateToSeries(string $sIcs, string $sRecurrenceId) : string
	{
		$bAllDay = false;
		foreach ($this->parseICalendar($sIcs) as $aEv) {
			if ('' === $aEv['recurrenceId']) {
				$bAllDay = !empty($aEv['allDay']);
				break;
			}
		}
		$sTarget = $this->parseICalDate($sRecurrenceId);
		$sExFmt = $this->formatIcsDateValue($sRecurrenceId, $bAllDay);

		$aLines = $this->unfoldIcs($sIcs);
		$iCount = \count($aLines);
		$aOut = [];
		$bMasterDone = false;

		for ($i = 0; $i < $iCount; $i++) {
			$sLine = \rtrim($aLines[$i]);
			if ('' === $sLine) {
				continue;
			}
			if (0 === \strcasecmp($sLine, 'BEGIN:VEVENT')) {
				$iEnd = $i;
				$bIsOverride = false;
				for ($j = $i + 1; $j < $iCount; $j++) {
					$sInner = \rtrim($aLines[$j]);
					if (0 === \strcasecmp($sInner, 'END:VEVENT')) {
						$iEnd = $j;
						break;
					}
					$sTrim = \ltrim($sInner);
					if (0 === \stripos($sTrim, 'RECURRENCE-ID') && false !== \strpos($sTrim, ':')) {
						if ($this->parseICalDate(\trim(\substr($sTrim, \strpos($sTrim, ':') + 1))) === $sTarget) {
							$bIsOverride = true;
						}
					}
				}
				if ($bIsOverride) {
					// Drop the override for the removed occurrence
					$i = $iEnd;
					continue;
				}
				if (!$bMasterDone) {
					$bMasterDone = true;
					for ($j = $i; $j <= $iEnd; $j++) {
						$sInner = \rtrim($aLines[$j]);
						if (0 === \strcasecmp($sInner, 'END:VEVENT')) {
							if ('' !== $sExFmt) {
								$aOut[] = 'EXDATE' . ($bAllDay ? ';VALUE=DATE' : '') . ':' . $sExFmt;
							}
						}
						$aOut[] = $sInner;
					}
					$i = $iEnd;
					continue;
				}
				for ($j = $i; $j <= $iEnd; $j++) {
					$aOut[] = \rtrim($aLines[$j]);
				}
				$i = $iEnd;
				continue;
			}
			$aOut[] = $sLine;
		}

		return \implode("\r\n", $aOut) . "\r\n";
	}

	/**
	 * Indexed list of VEVENT blocks inside an unfolded line array.
	 */
	private function getVEventBlocks(array $aLines) : array
	{
		$aBlocks = [];
		$iCount = \count($aLines);
		for ($i = 0; $i < $iCount; $i++) {
			if (0 === \strcasecmp(\rtrim($aLines[$i]), 'BEGIN:VEVENT')) {
				$j = $i + 1;
				while ($j < $iCount && 0 !== \strcasecmp(\rtrim($aLines[$j]), 'END:VEVENT')) {
					$j++;
				}
				$aBlocks[] = ['start' => $i, 'end' => \min($j, $iCount - 1)];
				$i = $j;
			}
		}
		return $aBlocks;
	}

	/**
	 * UID + normalised RECURRENCE-ID identifying a recurrence override block,
	 * or '' when the block is not an override.
	 */
	private function veventBlockKey(array $aLines, array $aBlock) : string
	{
		$sUid = '';
		$sRec = '';
		for ($k = $aBlock['start']; $k <= $aBlock['end']; $k++) {
			$sLine = \ltrim($aLines[$k]);
			$iPos = \strpos($sLine, ':');
			if (false === $iPos) {
				continue;
			}
			$sKey = \strtoupper(\strtok(\substr($sLine, 0, $iPos), ';'));
			$sVal = \trim(\substr($sLine, $iPos + 1));
			if ('UID' === $sKey) {
				$sUid = $sVal;
			} elseif ('RECURRENCE-ID' === $sKey) {
				$sRec = $this->parseICalDate($sVal);
			}
		}
		return '' === $sRec ? '' : ($sUid . '|' . $sRec);
	}

	/**
	 * Merge recurrence-override VEVENTs from $sIncoming into a stored resource.
	 * Used when a received invite updates/CANCELs a single recurring occurrence.
	 */
	private function mergeRecurrenceOverride(string $sBase, string $sIncoming) : string
	{
		$aBaseLines = $this->unfoldIcs($sBase);
		$aInLines = $this->unfoldIcs($sIncoming);
		$aBaseBlocks = $this->getVEventBlocks($aBaseLines);

		$aIncomingByKey = [];
		foreach ($this->getVEventBlocks($aInLines) as $aIn) {
			$sKey = $this->veventBlockKey($aInLines, $aIn);
			if ('' !== $sKey) {
				$aIncomingByKey[$sKey] = \array_slice($aInLines, $aIn['start'], $aIn['end'] - $aIn['start'] + 1);
			}
		}
		if (!$aIncomingByKey) {
			return $sIncoming;
		}

		$aOut = [];
		$iCount = \count($aBaseLines);
		$iBlockIndex = 0;
		$aEmitted = [];

		for ($i = 0; $i < $iCount; $i++) {
			$sLine = \rtrim($aBaseLines[$i]);
			$aBase = $aBaseBlocks[$iBlockIndex] ?? null;
			if ($aBase && $i === $aBase['start']) {
				$sKey = $this->veventBlockKey($aBaseLines, $aBase);
				if ('' !== $sKey && isset($aIncomingByKey[$sKey])) {
					foreach ($aIncomingByKey[$sKey] as $sAdd) {
						$aOut[] = $sAdd;
					}
					$aEmitted[$sKey] = true;
				} else {
					for ($k = $aBase['start']; $k <= $aBase['end']; $k++) {
						$aOut[] = \rtrim($aBaseLines[$k]);
					}
				}
				$i = $aBase['end'];
				$iBlockIndex++;
				continue;
			}
			if (0 === \strcasecmp($sLine, 'END:VCALENDAR')) {
				foreach ($aIncomingByKey as $sKey => $aAddLines) {
					if (empty($aEmitted[$sKey])) {
						foreach ($aAddLines as $sAdd) {
							$aOut[] = $sAdd;
						}
					}
				}
				$aOut[] = $sLine;
				continue;
			}
			$aOut[] = $sLine;
		}

		return \implode("\r\n", $aOut) . "\r\n";
	}

	/**
	 * Parse CalDAV XML response
	 */
	private function parseCalDAVResponse($xml)
	{
		$events = [];
		
		try {
			$doc = new \DOMDocument();
			$doc->loadXML($xml);
			
			$xpath = new \DOMXPath($doc);
			$xpath->registerNamespace('D', 'DAV:');
			$xpath->registerNamespace('C', 'urn:ietf:params:xml:ns:caldav');
			
			$responses = $xpath->query('//D:response');
			
			foreach ($responses as $response) {
				// The href identifies the exact calendar object resource
				$sHref = '';
				$hrefNodes = $xpath->query('./D:href', $response);
				if ($hrefNodes->length) {
					$sHref = trim($hrefNodes->item(0)->nodeValue);
				}
				$sEtag = '';
				$etagNodes = $xpath->query('.//D:getetag', $response);
				if ($etagNodes->length) {
					$sEtag = trim($etagNodes->item(0)->nodeValue);
				}
				$calendarData = $xpath->query('.//C:calendar-data', $response);
				if ($calendarData->length > 0) {
					$icalData = $calendarData->item(0)->nodeValue;
					$parsedEvents = $this->parseICalendar($icalData, $sHref, $sEtag);
					$events = array_merge($events, $parsedEvents);
				}
			}
		} catch (\Exception $e) {
			// Silent fail
		}
		
		return $events;
	}
	
	/* ------------------------------------------------------------------
	   Calendar invites received as .ics attachments in mail messages
	   ------------------------------------------------------------------ */

	/**
	 * Unfold an iCalendar text into an array of logical lines (RFC 5545 3.1).
	 *
	 * @return string[]
	 */
	private function unfoldIcs(string $sIcs) : array
	{
		$sIcs = \str_replace(["\r\n", "\r"], "\n", $sIcs);
		$aResult = [];
		foreach (\explode("\n", $sIcs) as $sLine) {
			if ('' !== $sLine && (' ' === $sLine[0] || "\t" === $sLine[0])) {
				if ($aResult) {
					$aResult[\count($aResult) - 1] .= \substr($sLine, 1);
				}
			} else {
				$aResult[] = $sLine;
			}
		}
		return $aResult;
	}

	/**
	 * Parse the first VEVENT of an iCalendar text (with its METHOD).
	 *
	 * @return array{method:string, properties:array<string, array<int, array{params:array, value:string}>>}
	 */
	private function parseInvite(string $sIcs) : array
	{
		$aResult = ['method' => '', 'properties' => []];
		$bInEvent = false;
		foreach ($this->unfoldIcs($sIcs) as $sLine) {
			if ('BEGIN:VEVENT' === $sLine) {
				$bInEvent = true;
				continue;
			}
			if ('END:VEVENT' === $sLine) {
				$bInEvent = false;
				continue;
			}
			$iPos = \strpos($sLine, ':');
			if (false === $iPos) {
				continue;
			}
			$sHead = \substr($sLine, 0, $iPos);
			$sValue = \substr($sLine, $iPos + 1);
			$aParts = \explode(';', $sHead);
			$sName = \strtoupper(\array_shift($aParts));
			if (!$bInEvent) {
				if ('METHOD' === $sName) {
					$aResult['method'] = \strtoupper(\trim($sValue));
				}
				continue;
			}
			$aParams = [];
			foreach ($aParts as $sParam) {
				$aPair = \explode('=', $sParam, 2);
				if (2 === \count($aPair)) {
					$aParams[\strtoupper(\trim($aPair[0]))] = \trim($aPair[1], '"');
				}
			}
			$aResult['properties'][$sName][] = ['params' => $aParams, 'value' => $sValue];
		}
		return $aResult;
	}

	/**
	 * First raw value of a VEVENT property.
	 */
	private function inviteProp(array $aInvite, string $sName, string $sDefault = '') : string
	{
		$sName = \strtoupper($sName);
		return isset($aInvite['properties'][$sName][0])
			? (string) $aInvite['properties'][$sName][0]['value']
			: $sDefault;
	}

	/**
	 * Extract the e-mail address from a CAL-ADDRESS / mailto value.
	 */
	private function inviteMailAddress(string $sValue) : string
	{
		$sValue = \trim($sValue);
		if (0 === \stripos($sValue, 'mailto:')) {
			$sValue = \substr($sValue, 7);
		}
		return \trim($sValue, " \t<>\"");
	}

	/**
	 * Escape a text value for use inside an iCalendar property.
	 */
	private function escapeICSText(string $sText) : string
	{
		return \str_replace(
			["\\", "\r\n", "\n", "\r", ",", ";"],
			["\\\\", "\\n", "\\n", "\\n", "\\,", "\\;"],
			$sText
		);
	}

	/**
	 * Unescape an iCalendar TEXT value (inverse of escapeICSText, RFC 5545
	 * 3.3.11). Uses strtr() so it is a single pass: a literal "\\" in the value
	 * is not confused with the "\n" / "\," / "\;" escapes.
	 */
	private function unescapeICSText(string $sText) : string
	{
		return \strtr($sText, [
			'\\n' => "\n",
			'\\N' => "\n",
			'\\,' => ',',
			'\\;' => ';',
			'\\\\' => '\\'
		]);
	}

	/**
	 * Rewrite a DTSTART/DTEND/RECURRENCE-ID/RDATE/EXDATE line that carries a
	 * TZID into an explicit UTC value, so the stored event does not depend on a
	 * VTIMEZONE component being present/preserved by the CalDAV server.
	 *
	 * @return string[] one or more lines (RDATE/EXDATE may hold a list)
	 */
	private function convertTzidLine(string $sLine) : array
	{
		$iPos = \strpos($sLine, ':');
		if (false === $iPos) {
			return [$sLine];
		}
		$sHead = \substr($sLine, 0, $iPos);
		if (false === \stripos($sHead, 'TZID=')) {
			return [$sLine];
		}
		$sName = \strtoupper(\strtok($sHead, ';'));
		if (!\in_array($sName, ['DTSTART', 'DTEND', 'RECURRENCE-ID', 'RDATE', 'EXDATE'], true)) {
			return [$sLine];
		}
		if (!\preg_match('/;TZID=([^;:]+)/i', $sHead, $tm)) {
			return [$sLine];
		}
		$sTzid = \trim($tm[1], '"');
		$sValue = \substr($sLine, $iPos + 1);
		$sNewHead = \preg_replace('/;TZID=[^;:]+/i', '', $sHead);

		$aOut = [];
		$bFailed = false;
		foreach (\explode(',', $sValue) as $sVal) {
			$sVal = \trim($sVal);
			if ('' === $sVal) {
				continue;
			}
			$sUtc = $this->tzidToUtcBasic($sVal, $sTzid);
			if (null === $sUtc) {
				$bFailed = true;
				break;
			}
			$aOut[] = $sNewHead . ':' . $sUtc;
		}
		if ($bFailed || !$aOut) {
			// Not a convertible local time (e.g. already UTC or a DATE):
			// keep the original line untouched.
			return [$sLine];
		}
		return $aOut;
	}

	/**
	 * Sanitize a received invite so it can be stored as a CalDAV resource:
	 * ensure UID/DTSTAMP, drop the iTIP METHOD and add the local user as attendee.
	 */
	private function prepareImportIcs(string $sIcs, string $sUid, string $sEmail) : string
	{
		$aOut = [];
		$bInEvent = false;
		$bHasUid = false;
		$bHasDtstamp = false;
		$bHasLocalAttendee = false;

		foreach ($this->unfoldIcs($sIcs) as $sLine) {
			if ('' === \trim($sLine)) {
				continue;
			}
			if (0 === \stripos($sLine, 'METHOD:')) {
				// METHOD is only used for iTIP transport, not stored on objects
				continue;
			}
			if ('BEGIN:VEVENT' === $sLine) {
				$bInEvent = true;
				$bHasUid = false;
				$bHasDtstamp = false;
				$bHasLocalAttendee = false;
				$aOut[] = $sLine;
				continue;
			}
			if ('END:VEVENT' === $sLine) {
				if ($bInEvent) {
					if (!$bHasUid) {
						$aOut[] = 'UID:' . $sUid;
					}
					if (!$bHasDtstamp) {
						$aOut[] = 'DTSTAMP:' . \gmdate('Ymd\THis\Z');
					}
					if (!$bHasLocalAttendee && $sEmail) {
						$aOut[] = 'ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:' . $sEmail;
					}
				}
				$aOut[] = $sLine;
				$bInEvent = false;
				continue;
			}
			if ($bInEvent) {
				$sUpper = \strtoupper($sLine);
				if (0 === \strpos($sUpper, 'UID')) {
					$bHasUid = true;
				} else if (0 === \strpos($sUpper, 'DTSTAMP')) {
					$bHasDtstamp = true;
				} else if (0 === \strpos($sUpper, 'ATTENDEE') && false !== \stripos($sLine, $sEmail)) {
					$bHasLocalAttendee = true;
				}
			}
			foreach ($this->convertTzidLine($sLine) as $sConverted) {
				$aOut[] = $sConverted;
			}
		}

		$sOut = \implode("\r\n", $aOut) . "\r\n";
		// When every TZID reference has been resolved to UTC the VTIMEZONE
		// blocks are no longer needed; drop them so the stored event looks the
		// same as an event created directly by the plugin.
		if (false === \stripos($sOut, 'TZID=')) {
			$sOut = $this->removeVtimezoneBlocks($sOut);
		}
		return $sOut;
	}

	/**
	 * Remove all VTIMEZONE components from an iCalendar text.
	 */
	private function removeVtimezoneBlocks(string $sIcs) : string
	{
		$aOut = [];
		$bInVtimezone = false;
		foreach ($this->unfoldIcs($sIcs) as $sLine) {
			if (0 === \stripos($sLine, 'BEGIN:VTIMEZONE')) {
				$bInVtimezone = true;
				continue;
			}
			if (0 === \stripos($sLine, 'END:VTIMEZONE')) {
				$bInVtimezone = false;
				continue;
			}
			if ($bInVtimezone) {
				continue;
			}
			$aOut[] = $sLine;
		}
		return \implode("\r\n", $aOut) . "\r\n";
	}

	/**
	 * Build an iTIP METHOD:REPLY calendar object for the current user.
	 */
	private function buildReplyIcs(string $sUid, string $sOrganizer, string $sAttendee,
		string $sPartstat, string $sSummary, string $sSequence, string $sRecurrenceId = '') : string
	{
		$sOrganizerValue = (0 === \stripos($sOrganizer, 'mailto:')) ? $sOrganizer : 'mailto:' . $sOrganizer;

		$sIcs = "BEGIN:VCALENDAR\r\n";
		$sIcs .= "VERSION:2.0\r\n";
		$sIcs .= "PRODID:-//Mailbux//CalDAV Plugin//EN\r\n";
		$sIcs .= "METHOD:REPLY\r\n";
		$sIcs .= "BEGIN:VEVENT\r\n";
		$sIcs .= "UID:" . $this->escapeICSText($sUid) . "\r\n";
		$sIcs .= "DTSTAMP:" . \gmdate('Ymd\THis\Z') . "\r\n";
		if ('' !== $sRecurrenceId) {
			$sIcs .= "RECURRENCE-ID:" . $this->escapeICSText($sRecurrenceId) . "\r\n";
		}
		if ('' !== $sSequence && \ctype_digit($sSequence)) {
			$sIcs .= "SEQUENCE:" . $sSequence . "\r\n";
		}
		if ('' !== $sSummary) {
			$sIcs .= "SUMMARY:" . $this->escapeICSText($sSummary) . "\r\n";
		}
		$sIcs .= "ORGANIZER:" . $sOrganizerValue . "\r\n";
		$sIcs .= "ATTENDEE;PARTSTAT=" . $sPartstat . ":mailto:" . $sAttendee . "\r\n";
		$sIcs .= "END:VEVENT\r\n";
		$sIcs .= "END:VCALENDAR\r\n";

		return $sIcs;
	}

	/**
	 * Add the event to the requested calendar collection.
	 */
	public function DoImportCalendarEvent() : array
	{
		try {
			if (!$this->Config()->Get('plugin', 'allow_invites', true)) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_INVITES_DISABLED', [], 'Calendar invites are disabled')]);
			}

			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if (!$oAccount) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_LOGGED_IN', [], 'Not logged in')]);
			}

			$aConfig = $this->getCalendarConfig($oAccount);
			if (!$aConfig) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_CONFIGURED', [], 'Calendar not configured')]);
			}

			$sIcs = (string) $this->jsonParam('Ics', '');
			if ('' === \trim($sIcs) || false === \stripos($sIcs, 'BEGIN:VEVENT')) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_ICS_INVALID', [], 'Invalid calendar invite')]);
			}

			$aInvite = $this->parseInvite($sIcs);
			$sUid = $this->inviteProp($aInvite, 'UID');
			if ('' === $sUid) {
				$sUid = \uniqid('invite-', true) . '@' . \MailSo\Base\Utils::Sha1Rand($aConfig['User']);
			}

			$sIcs = $this->prepareImportIcs($sIcs, $sUid, $oAccount->Email());

			$sPassword = $this->getDecryptedPassword($aConfig);
			if (null === $sPassword) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NO_ENCRYPTION_KEY', [], 'Cannot access encryption key')]);
			}

			$sCalendarId = $this->jsonParam('CalendarId', 'default');
			$sEventUrl = $this->calendarUrl($aConfig, $sCalendarId) . '/' . \rawurlencode($sUid) . '.ics';

			// A per-occurrence update (RECURRENCE-ID) must be merged into the
			// existing series resource instead of overwriting the whole series.
			$sRecurrenceId = $this->inviteProp($aInvite, 'RECURRENCE-ID');
			if ('' !== $sRecurrenceId) {
				$getResult = $this->makeCalDAVRequest($sEventUrl, 'GET', $aConfig['User'], $sPassword);
				if (200 === $getResult['code'] && false !== \stripos((string) $getResult['body'], 'BEGIN:VEVENT')) {
					$sIcs = $this->mergeRecurrenceOverride((string) $getResult['body'], $sIcs);
				}
			}

			$result = $this->makeCalDAVRequest(
				$sEventUrl,
				'PUT',
				$aConfig['User'],
				$sPassword,
				$sIcs,
				['Content-Type: text/calendar; charset=utf-8']
			);

			if (201 === $result['code'] || 204 === $result['code'] || 200 === $result['code']) {
				return $this->jsonResponse(__FUNCTION__, [
					'success' => true,
					'uid' => $sUid,
					'calendarId' => $sCalendarId
				]);
			}

			return $this->jsonResponse(__FUNCTION__, [
				'success' => false,
				'error' => $this->msg('ERROR_CALDAV', ['CODE' => $result['code']], 'CalDAV error: ' . $result['code'])
			]);

		} catch (\Exception $e) {
			return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $e->getMessage()]);
		}
	}

	/**
	 * Respond to an invite: send an iTIP REPLY to the organizer and, when the
	 * event has been added to a calendar, update its PARTSTAT.
	 */
	public function DoRespondToEvent() : array
	{
		try {
			if (!$this->Config()->Get('plugin', 'allow_invites', true)) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_INVITES_DISABLED', [], 'Calendar invites are disabled')]);
			}

			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if (!$oAccount) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_LOGGED_IN', [], 'Not logged in')]);
			}

			$sResponse = \strtoupper((string) $this->jsonParam('Response', ''));
			if (!\in_array($sResponse, ['ACCEPTED', 'TENTATIVE', 'DECLINED'], true)) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_INVALID_RESPONSE', [], 'Invalid response')]);
			}

			$sIcs = (string) $this->jsonParam('Ics', '');
			if ('' === \trim($sIcs) || false === \stripos($sIcs, 'BEGIN:VEVENT')) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_ICS_INVALID', [], 'Invalid calendar invite')]);
			}

			$aInvite = $this->parseInvite($sIcs);
			$sUid = $this->inviteProp($aInvite, 'UID');
			$sOrganizer = $this->inviteMailAddress($this->inviteProp($aInvite, 'ORGANIZER'));
			$sSummary = $this->inviteProp($aInvite, 'SUMMARY');
			$sSequence = $this->inviteProp($aInvite, 'SEQUENCE');
			$sRecurrenceId = \trim((string) $this->jsonParam('RecurrenceId', $this->inviteProp($aInvite, 'RECURRENCE-ID')));

			if ('' === $sOrganizer) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NO_ORGANIZER', [], 'No organizer to reply to')]);
			}

			// Prefer the exact attendee address that matches this account (if any)
			$sEmail = $oAccount->Email();
			foreach ($aInvite['properties']['ATTENDEE'] ?? [] as $aAttendee) {
				$sAttendeeMail = $this->inviteMailAddress($aAttendee['value']);
				if ('' !== $sAttendeeMail && 0 === \strcasecmp($sAttendeeMail, $sEmail)) {
					$sEmail = $sAttendeeMail;
					break;
				}
			}

			$sReplyIcs = $this->buildReplyIcs($sUid, $sOrganizer, $sEmail, $sResponse, $sSummary, $sSequence, $sRecurrenceId);

			$this->sendInviteReply($oAccount, $sOrganizer, $sEmail, $sSummary, $sResponse, $sReplyIcs);

			// Update PARTSTAT on the stored event when we know where it lives
			$sCalendarId = (string) $this->jsonParam('CalendarId', '');
			$sEventUid = (string) $this->jsonParam('Uid', $sUid);
			if ('' !== $sCalendarId && '' !== $sEventUid) {
				$this->updateEventPartstat($oAccount, $sCalendarId, $sEventUid, $sEmail, $sResponse, $sRecurrenceId);
			}

			return $this->jsonResponse(__FUNCTION__, ['success' => true, 'response' => $sResponse]);

		} catch (\Exception $e) {
			return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $e->getMessage()]);
		}
	}

	/**
	 * Remove an event from a calendar. Used to action a received iTIP CANCEL,
	 * and to delete a recurring event either as a whole series or a single
	 * occurrence (excluded via EXDATE).
	 */
	public function DoRemoveCalendarEvent() : array
	{
		try {
			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if (!$oAccount) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_LOGGED_IN', [], 'Not logged in')]);
			}

			$aConfig = $this->getCalendarConfig($oAccount);
			if (!$aConfig) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NOT_CONFIGURED', [], 'Calendar not configured')]);
			}

			$sEventId = (string) $this->jsonParam('EventId', '');
			$sCalendarId = (string) $this->jsonParam('CalendarId', 'default');
			$sEventUrlParam = (string) $this->jsonParam('EventUrl', '');
			$sRecurrenceId = \trim((string) $this->jsonParam('RecurrenceId', ''));
			$sMode = \strtolower(\trim((string) $this->jsonParam('Mode', 'series')));

			if ('' === $sEventId) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_EVENT_ID_REQUIRED', [], 'Event ID required')]);
			}

			$sPassword = $this->getDecryptedPassword($aConfig);
			if (null === $sPassword) {
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_NO_ENCRYPTION_KEY', [], 'Cannot access encryption key')]);
			}

			$sEventUrl = $this->resolveEventUrl($aConfig, $sEventUrlParam);
			if ('' === $sEventUrl) {
				$sEventUrl = $this->calendarUrl($aConfig, $sCalendarId) . '/' . \rawurlencode($sEventId) . '.ics';
			}

			if ('occurrence' === $sMode && '' !== $sRecurrenceId) {
				$getResult = $this->makeCalDAVRequest($sEventUrl, 'GET', $aConfig['User'], $sPassword);
				if (200 !== $getResult['code'] || empty($getResult['body'])) {
					return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_CALDAV', ['CODE' => $getResult['code']], 'CalDAV error: ' . $getResult['code'])]);
				}
				$sNewIcs = $this->addExdateToSeries((string) $getResult['body'], $sRecurrenceId);
				$putResult = $this->makeCalDAVRequest($sEventUrl, 'PUT', $aConfig['User'], $sPassword, $sNewIcs, ['Content-Type: text/calendar; charset=utf-8']);
				if (\in_array($putResult['code'], [200, 201, 204], true)) {
					return $this->jsonResponse(__FUNCTION__, ['success' => true, 'mode' => 'occurrence']);
				}
				return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_CALDAV', ['CODE' => $putResult['code']], 'CalDAV error: ' . $putResult['code'])]);
			}

			$result = $this->makeCalDAVRequest($sEventUrl, 'DELETE', $aConfig['User'], $sPassword);
			if (\in_array($result['code'], [200, 204, 404], true)) {
				return $this->jsonResponse(__FUNCTION__, ['success' => true, 'mode' => 'series']);
			}
			return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $this->msg('ERROR_CALDAV', ['CODE' => $result['code']], 'CalDAV error: ' . $result['code'])]);

		} catch (\Exception $e) {
			return $this->jsonResponse(__FUNCTION__, ['success' => false, 'error' => $e->getMessage()]);
		}
	}

	/**
	 * Send the iTIP REPLY to the organizer using the account's SMTP settings.
	 */
	private function sendInviteReply(\RainLoop\Model\Account $oAccount, string $sTo,
		string $sFrom, string $sSummary, string $sResponse, string $sReplyIcs) : void
	{
		$aStatus = [
			'ACCEPTED' => $this->msg('REPLY_STATUS_ACCEPTED', [], 'Accepted'),
			'TENTATIVE' => $this->msg('REPLY_STATUS_TENTATIVE', [], 'Tentative'),
			'DECLINED' => $this->msg('REPLY_STATUS_DECLINED', [], 'Declined')
		];
		$sStatus = $aStatus[$sResponse] ?? $sResponse;
		$sSubject = $sStatus . ($sSummary ? ': ' . $sSummary : '');

		$sDisplayName = $oAccount->Name() ?: $sFrom;
		$sBody = \sprintf(
			'%s - %s%s',
			$sDisplayName,
			$sStatus,
			($sSummary ? ': ' . $sSummary : '')
		);

		$sBoundary = 'mailbux-caldav-' . \MailSo\Base\Utils::Sha1Rand('boundary');
		$sEol = "\r\n";

		$aHeaders = [
			'From: ' . \MailSo\Base\Utils::EncodeHeaderValue($sDisplayName) . ' <' . $sFrom . '>',
			'To: <' . $sTo . '>',
			'Subject: ' . \MailSo\Base\Utils::EncodeHeaderValue($sSubject),
			'Date: ' . \gmdate('r'),
			'Message-ID: ' . \sprintf('<%s@%s>', \MailSo\Base\Utils::Sha1Rand($sFrom . \microtime()), 'mailbux'),
			'MIME-Version: 1.0',
			'Content-Type: multipart/mixed; boundary="' . $sBoundary . '"'
		];

		$sRaw = \implode($sEol, $aHeaders) . $sEol . $sEol;
		$sRaw .= '--' . $sBoundary . $sEol;
		$sRaw .= 'Content-Type: text/plain; charset="utf-8"' . $sEol;
		$sRaw .= 'Content-Transfer-Encoding: 8bit' . $sEol . $sEol;
		$sRaw .= \preg_replace('/\r?\n/', $sEol, \trim($sBody)) . $sEol;
		$sRaw .= '--' . $sBoundary . $sEol;
		$sRaw .= 'Content-Type: text/calendar; charset="utf-8"; method=REPLY; name="invite.ics"' . $sEol;
		$sRaw .= 'Content-Disposition: attachment; filename="invite.ics"' . $sEol;
		$sRaw .= 'Content-Transfer-Encoding: base64' . $sEol . $sEol;
		$sRaw .= \chunk_split(\base64_encode($sReplyIcs), 76, $sEol);
		$sRaw .= '--' . $sBoundary . '--' . $sEol;

		$oActions = $this->Manager()->Actions();

		$oSmtpClient = new \MailSo\Smtp\SmtpClient();
		try {
			$oSmtpClient->SetLogger(\RainLoop\Api::Logger());
		} catch (\Throwable $e) {
			// Logger is optional
		}

		$oAccount->SmtpConnectAndLogin($oActions->Plugins(), $oSmtpClient);

		if ($oSmtpClient->Settings->usePhpMail) {
			list($sRawHeaders, $sRawBody) = \explode("\r\n\r\n", $sRaw, 2);
			$bSent = \MailSo\Base\Utils::FunctionCallable('mail')
				? \mail($sTo, $sSubject, $sRawBody, $sRawHeaders, '-f' . $sFrom)
				: false;
			if (!$bSent) {
				throw new \RuntimeException('Failed to send the calendar reply');
			}
			return;
		}

		$oSmtpClient->MailFrom($sFrom);
		$oSmtpClient->Rcpt($sTo);
		$oSmtpClient->Data($sRaw);
		$oSmtpClient->Disconnect();
	}

	/**
	 * Best-effort update of the local attendee PARTSTAT on a stored event.
	 */
	private function updateEventPartstat(\RainLoop\Model\Account $oAccount, string $sCalendarId,
		string $sUid, string $sEmail, string $sPartstat, string $sRecurrenceId = '') : void
	{
		try {
			$aConfig = $this->getCalendarConfig($oAccount);
			if (!$aConfig) {
				return;
			}
			$sPassword = $this->getDecryptedPassword($aConfig);
			if (null === $sPassword) {
				return;
			}

			$sEventUrl = $this->calendarUrl($aConfig, $sCalendarId) . '/' . \rawurlencode($sUid) . '.ics';
			$result = $this->makeCalDAVRequest($sEventUrl, 'GET', $aConfig['User'], $sPassword);
			if (200 !== $result['code'] || empty($result['body'])) {
				return;
			}

			$sIcs = (string) $result['body'];
			if (false === \stripos($sIcs, 'BEGIN:VEVENT')) {
				return;
			}

			// When a RECURRENCE-ID is given only that override is updated,
			// otherwise the master VEVENT is updated.
			$sTargetRec = '' === $sRecurrenceId ? null : $this->parseICalDate($sRecurrenceId);

			$aOut = [];
			$bUpdated = false;
			$bInEvent = false;
			$bInTarget = false;
			foreach ($this->unfoldIcs($sIcs) as $sLine) {
				$sTrim = \rtrim($sLine);
				if (0 === \strcasecmp($sTrim, 'BEGIN:VEVENT')) {
					$bInEvent = true;
					$bInTarget = false;
					$aOut[] = $sTrim;
					continue;
				}
				if (0 === \strcasecmp($sTrim, 'END:VEVENT')) {
					$bInEvent = false;
					$bInTarget = false;
					$aOut[] = $sTrim;
					continue;
				}
				if ($bInEvent) {
					$sTrimLead = \ltrim($sTrim);
					$iColon = \strpos($sTrimLead, ':');
					$sKey = (false !== $iColon)
						? \strtoupper(\strtok(\substr($sTrimLead, 0, $iColon), ';'))
						: '';
					if ('RECURRENCE-ID' === $sKey) {
						$bInTarget = (null !== $sTargetRec)
							&& ($this->parseICalDate(\trim(\substr($sTrimLead, $iColon + 1))) === $sTargetRec);
						$aOut[] = $sTrim;
						continue;
					}
					if ('UID' === $sKey && null === $sTargetRec) {
						$bInTarget = true;
						$aOut[] = $sTrim;
						continue;
					}
				}
				if ($bInTarget && 0 === \stripos($sTrim, 'ATTENDEE') && false !== \stripos($sTrim, $sEmail)) {
					if (\preg_match('/PARTSTAT=[^;:]+/i', $sTrim)) {
						$sTrim = \preg_replace('/PARTSTAT=[^;:]+/i', 'PARTSTAT=' . $sPartstat, $sTrim);
					} else {
						$iPos = \strpos($sTrim, ':');
						if (false !== $iPos) {
							$sTrim = \substr($sTrim, 0, $iPos) . ';PARTSTAT=' . $sPartstat . \substr($sTrim, $iPos);
						}
					}
					$bUpdated = true;
				}
				$aOut[] = $sTrim;
			}

			if (!$bUpdated) {
				return;
			}

			$this->makeCalDAVRequest(
				$sEventUrl,
				'PUT',
				$aConfig['User'],
				$sPassword,
				\implode("\r\n", $aOut) . "\r\n",
				['Content-Type: text/calendar; charset=utf-8']
			);
		} catch (\Exception $e) {
			// Best-effort only - the reply e-mail is the important part
		}
	}
}
