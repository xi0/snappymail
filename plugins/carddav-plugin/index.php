<?php

class MailbuxCardDAVAutoPlugin extends \RainLoop\Plugins\AbstractPlugin
{
	const
		NAME     = 'Mailbux CardDAV Auto',
		VERSION  = '1.5',
		RELEASE  = '2025-11-12',
		CATEGORY = 'Contacts',
		DESCRIPTION = 'Auto-configures CardDAV sync - switches per account',
		REQUIRED = '2.0.0';
	
	private $lastConfiguredEmail = null;

	public function Init() : void
	{
		// Hook into login.success - runs for main login AND added accounts
		$this->addHook('login.success', 'AutoConfigureCardDAV');
		
		// Hook after AccountSwitch JSON action completes
		$this->addHook('json.after-AccountSwitch', 'OnAfterAccountSwitch');
	}
	
	/**
	 * Called after AccountSwitch action completes
	 */
	public function OnAfterAccountSwitch(array &$aResponse)
	{
		if (!empty($aResponse['Result'])) {
			// Account switch succeeded - clear last email to force update
			$this->lastConfiguredEmail = null;
			
			// Update CardDAV for switched account
			$oAccount = $this->Manager()->Actions()->getAccountFromToken();
			if ($oAccount) {
				$this->AutoConfigureCardDAV($oAccount);
			}
		}
	}

	/**
	 * Auto-configure CardDAV sync for current account
	 */
	public function AutoConfigureCardDAV(\RainLoop\Model\Account $oAccount)
	{
		if (!$oAccount || !$oAccount->Email()) {
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
			// Get storage provider
			$oStorageProvider = $oActions->StorageProvider();
			if (!$oStorageProvider) {
				return;
			}

			// Always update CardDAV to match current account email
			$sCardDAVUrl = "https://my.mailbux.com/dav/card/{$sEmail}/default";
			
			// Get account credentials
			$sPassword = null;
			$sPasswordHMAC = null;
			
			// Try to get password from additionalaccounts file
			$aAdditionalAccounts = $this->getAdditionalAccounts($oAccount, $oStorageProvider);
			
			if (isset($aAdditionalAccounts[$sEmail])) {
				// Found in additional accounts - convert password format
				try {
					// Get the main account (which has CryptKey method)
					$oMainAccount = $this->Manager()->Actions()->GetMainAccountFromToken();
					if (!$oMainAccount) {
						return;
					}
					
					$sCryptKey = $oMainAccount->CryptKey();
					
					// Decrypt using DecryptUrlSafe (dot-separated format)
					$sRawPassword = \SnappyMail\Crypt::DecryptUrlSafe($aAdditionalAccounts[$sEmail]['pass'], $sCryptKey);
					
					// Convert SensitiveString to string if needed
					if (is_object($sRawPassword) && method_exists($sRawPassword, '__toString')) {
						$sRawPassword = (string)$sRawPassword;
					}
					
					if (!$sRawPassword) {
						return;
					}
					
					// Re-encrypt in JSON array format for contacts_sync
					$sPassword = \SnappyMail\Crypt::EncryptToJSON($sRawPassword, $sCryptKey);
					$sPasswordHMAC = \hash_hmac('sha1', $sPassword, $sCryptKey);
				} catch (\Throwable $e) {
					return; // Skip update if password conversion fails
				}
			} else {
				// Primary account - encrypt password
				$sRawPassword = $oAccount->ImapPass();
				$sCryptKey = $oAccount->CryptKey();
				$sPassword = \SnappyMail\Crypt::EncryptToJSON($sRawPassword, $sCryptKey);
				$sPasswordHMAC = \hash_hmac('sha1', $sPassword, $sCryptKey);
			}
			
			// Prepare CardDAV data
			$aCardDAVData = [
				'Mode' => 1,
				'User' => $sEmail,
				'Password' => $sPassword,
				'PasswordHMAC' => $sPasswordHMAC,
				'Url' => $sCardDAVUrl
			];
			
			// Save CardDAV sync data
			$oStorageProvider->Put($oAccount,
				\RainLoop\Providers\Storage\Enumerations\StorageType::CONFIG,
				'contacts_sync',
				\json_encode($aCardDAVData)
			);
			
		} catch (\Exception $e) {
			// Silent fail
		}
	}
	
	/**
	 * Get additional accounts from storage
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
}
