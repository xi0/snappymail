// Auto-switch to newly added account and handle CardDAV updates
(() => {
	'use strict';

	console.log('[Mailbux CardDAV] Auto-switch handler loaded');

	// Listen for account add/login events
	addEventListener('rl-ajax-response', (e) => {
		if (!e.detail) return;

		const action = e.detail.Action;
		const result = e.detail.Result;

		// After successfully adding an additional account, switch to it
		if (action === 'AccountSetup' && result && result.Email) {
			console.log('[Mailbux CardDAV] New account added:', result.Email);
			
			// Wait a moment for the account to be fully added
			setTimeout(() => {
				console.log('[Mailbux CardDAV] Switching to newly added account:', result.Email);
				
				// Trigger account switch
				rl.app.AccountList().find(acc => {
					if (acc.email === result.Email) {
						console.log('[Mailbux CardDAV] Found account, switching...');
						rl.app.accountsAndIdentities.accountForEdit(acc);
						acc.switch();
						return true;
					}
					return false;
				});
			}, 500);
		}
	});
})();
