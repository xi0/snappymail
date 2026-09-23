<?php

class CaldavPlugin extends \RainLoop\Plugins\AbstractPlugin
{
	const
		NAME     = 'Mailbux CalDAV Auto',
		VERSION  = '1.8',
		RELEASE  = '2025-11-20',
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

		// Add JavaScript
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
	 * Parse iCalendar data
	 */
	private function parseICalendar($icalData, $sHref = '')
	{
		$events = [];
		
		// Simple iCalendar parser
		$lines = explode("\n", str_replace("\r\n", "\n", $icalData));
		$currentEvent = null;
		
		foreach ($lines as $line) {
			$line = trim($line);
			
			if ($line === 'BEGIN:VEVENT') {
				$currentEvent = [];
			} elseif ($line === 'END:VEVENT' && $currentEvent !== null) {
				// Map to expected format
				$event = [
					'uid' => $currentEvent['uid'] ?? '',
					'summary' => $currentEvent['summary'] ?? 'Untitled',
					'dtstart' => $this->parseICalDate($currentEvent['dtstart'] ?? ''),
					'dtend' => $this->parseICalDate($currentEvent['dtend'] ?? ''),
					'description' => $currentEvent['description'] ?? '',
					'location' => $currentEvent['location'] ?? '',
					'allDay' => !isset($currentEvent['dtstart']) || strpos($currentEvent['dtstart'], 'T') === false,
					'href' => $sHref
				];
				$events[] = $event;
				$currentEvent = null;
			} elseif ($currentEvent !== null && strpos($line, ':') !== false) {
				list($key, $value) = explode(':', $line, 2);
				// Handle properties with parameters (e.g., DTSTART;VALUE=DATE:20251112)
				$key = preg_replace('/;.*$/', '', $key);
				$currentEvent[strtolower($key)] = $value;
			}
		}
		
		return $events;
	}
	
	/**
	 * Parse iCalendar date format to ISO string
	 */
	private function parseICalDate($dateStr)
	{
		if (empty($dateStr)) {
			return date('c');
		}
		
		// Handle YYYYMMDD format
		if (preg_match('/^(\d{4})(\d{2})(\d{2})$/', $dateStr, $matches)) {
			return $matches[1] . '-' . $matches[2] . '-' . $matches[3];
		}
		
		// Handle YYYYMMDDTHHmmssZ format
		if (preg_match('/^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z?$/', $dateStr, $matches)) {
			return $matches[1] . '-' . $matches[2] . '-' . $matches[3] . 'T' . 
			       $matches[4] . ':' . $matches[5] . ':' . $matches[6] . 'Z';
		}
		
		return $dateStr;
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
			
			// Create updated iCalendar
			$sICS = "BEGIN:VCALENDAR\r\n";
			$sICS .= "VERSION:2.0\r\n";
			$sICS .= "PRODID:-//Mailbux//CalDAV Plugin//EN\r\n";
			$sICS .= "BEGIN:VEVENT\r\n";
			$sICS .= "UID:" . $sEventId . "\r\n";
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
			$sICS .= "END:VEVENT\r\n";
			$sICS .= "END:VCALENDAR\r\n";
			
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
			
			// PUT updated event (reuse the server-reported resource URL when available)
			$sEventUrl = $this->resolveEventUrl($aConfig, $sEventUrlParam);
			if ('' === $sEventUrl) {
				$sEventUrl = $this->calendarUrl($aConfig, $sCalendarId) . '/' . rawurlencode($sEventId) . '.ics';
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
				$calendarData = $xpath->query('.//C:calendar-data', $response);
				if ($calendarData->length > 0) {
					$icalData = $calendarData->item(0)->nodeValue;
					$parsedEvents = $this->parseICalendar($icalData, $sHref);
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
			$aOut[] = $sLine;
		}

		return \implode("\r\n", $aOut) . "\r\n";
	}

	/**
	 * Build an iTIP METHOD:REPLY calendar object for the current user.
	 */
	private function buildReplyIcs(string $sUid, string $sOrganizer, string $sAttendee,
		string $sPartstat, string $sSummary, string $sSequence) : string
	{
		$sOrganizerValue = (0 === \stripos($sOrganizer, 'mailto:')) ? $sOrganizer : 'mailto:' . $sOrganizer;

		$sIcs = "BEGIN:VCALENDAR\r\n";
		$sIcs .= "VERSION:2.0\r\n";
		$sIcs .= "PRODID:-//Mailbux//CalDAV Plugin//EN\r\n";
		$sIcs .= "METHOD:REPLY\r\n";
		$sIcs .= "BEGIN:VEVENT\r\n";
		$sIcs .= "UID:" . $this->escapeICSText($sUid) . "\r\n";
		$sIcs .= "DTSTAMP:" . \gmdate('Ymd\THis\Z') . "\r\n";
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

			$sReplyIcs = $this->buildReplyIcs($sUid, $sOrganizer, $sEmail, $sResponse, $sSummary, $sSequence);

			$this->sendInviteReply($oAccount, $sOrganizer, $sEmail, $sSummary, $sResponse, $sReplyIcs);

			// Update PARTSTAT on the stored event when we know where it lives
			$sCalendarId = (string) $this->jsonParam('CalendarId', '');
			$sEventUid = (string) $this->jsonParam('Uid', $sUid);
			if ('' !== $sCalendarId && '' !== $sEventUid) {
				$this->updateEventPartstat($oAccount, $sCalendarId, $sEventUid, $sEmail, $sResponse);
			}

			return $this->jsonResponse(__FUNCTION__, ['success' => true, 'response' => $sResponse]);

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
		string $sUid, string $sEmail, string $sPartstat) : void
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

			$aOut = [];
			$bUpdated = false;
			foreach ($this->unfoldIcs($sIcs) as $sLine) {
				if (0 === \stripos($sLine, 'ATTENDEE') && false !== \stripos($sLine, $sEmail)) {
					if (\preg_match('/PARTSTAT=[^;:]+/i', $sLine)) {
						$sLine = \preg_replace('/PARTSTAT=[^;:]+/i', 'PARTSTAT=' . $sPartstat, $sLine);
					} else {
						$iPos = \strpos($sLine, ':');
						if (false !== $iPos) {
							$sLine = \substr($sLine, 0, $iPos) . ';PARTSTAT=' . $sPartstat . \substr($sLine, $iPos);
						}
					}
					$bUpdated = true;
				}
				$aOut[] = $sLine;
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
