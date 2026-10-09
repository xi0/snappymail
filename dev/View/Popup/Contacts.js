import { addObservablesTo, addComputablesTo } from 'External/ko';
import { ComposeType } from 'Common/EnumsUser';
import { registerShortcut } from 'Common/Globals';
import { arrayLength, pInt } from 'Common/Utils';
import { download, showMessageComposer } from 'Common/UtilsUser';

import { Selector } from 'Common/Selector';
import { serverRequestRaw, serverRequest } from 'Common/Links';
import { i18n, getNotification } from 'Common/Translator';

import { SettingsUserStore } from 'Stores/User/Settings';
import { ContactUserStore } from 'Stores/User/Contact';

import Remote from 'Remote/User/Fetch';

import { EmailModel } from 'Model/Email';
import { ContactModel } from 'Model/Contact';

import { decorateKoCommands } from 'Knoin/Knoin';
import { AbstractViewPopup } from 'Knoin/AbstractViews';

import { AskPopupView } from 'View/Popup/Ask';

const
	CONTACTS_PER_PAGE = 50,
	ScopeContacts = 'Contacts';

let
	bOpenCompose = false,
	sComposeRecipientsField = '';

export class ContactsPopupView extends AbstractViewPopup {
	constructor() {
		super('Contacts');

		addObservablesTo(this, {
			search: '',
			contactsCount: 0,

			selectorContact: null,

			importButton: null,

			isSaving: false,

			contact: null,

			// Endless scroll: server offset of the next chunk to load
			listOffset: 0,
			// Endless scroll: all contacts of the current list are loaded
			endReached: false,
			// Endless scroll: a next chunk is being loaded
			loadingMore: false
		});

		this.contacts = ContactUserStore;

		this.useCheckboxesInList = SettingsUserStore.useCheckboxesInList;

		this.selector = new Selector(
			ContactUserStore,
			this.selectorContact,
			null,
			'.e-contact-item',
			'.e-contact-item .checkboxItem'
		);

		this.selector.on('ItemSelect', contact => this.populateViewContact(contact));

		this.selector.on('ItemGetUid', contact => contact ? contact.id() : '');

		addComputablesTo(this, {
			contactsCheckedOrSelected: () => {
				const checked = ContactUserStore.filter(item => item.checked()),
					selected = this.selectorContact();
				return checked.length ? checked : (selected ? [selected] : []);
			},

			contactsSyncEnabled: () => ContactUserStore.allowSync() && ContactUserStore.syncMode(),

			// Endless scroll: are there more contacts on the server to load?
			canLoadMore: () => !this.endReached()
				&& ContactUserStore().length < this.contactsCount(),

			isBusy: () => ContactUserStore.syncing() | ContactUserStore.importing() | ContactUserStore.loading()
				| this.isSaving()
		});

		this.search.subscribe(() => this.reloadContactList());

		this.saveCommand = this.saveCommand.bind(this);

		decorateKoCommands(this, {
			deleteCommand: self => !self.isBusy() && 0 < self.contactsCheckedOrSelected().length,
			newMessageCommand: self => !self.isBusy() && 0 < self.contactsCheckedOrSelected().length,
			saveCommand: self => !self.isBusy(),
			syncCommand: self => !self.isBusy()
		});
	}

	newContact() {
		this.populateViewContact(new ContactModel);
		this.selectorContact(null);
	}

	deleteCommand() {
		const contacts = this.contactsCheckedOrSelected();
		if (contacts.length) {
			let selectorContact = this.selectorContact(),
				uids = [];
			contacts.forEach(contact => {
				uids.push(contact.id());
				if (selectorContact && selectorContact.id() === contact.id()) {
					this.selectorContact(selectorContact = null);
				}
				contact.deleted(true);
			});
			Remote.request('ContactsDelete',
				(iError, oData) => {
					if (iError) {
						alert(oData?.message || getNotification(iError));
					}
//					else {
//						contacts.forEach(contact => ContactUserStore.remove(contact));
//					}
					this.reloadContactList();
				}, {
					uids: uids.join(',')
				}
			);
		}
	}

	newMessageCommand() {
		let aE = [],
			recipients = {to:null,cc:null,bcc:null};

		this.contactsCheckedOrSelected().forEach(oContact => {
			if (oContact) {
				let name = (oContact.givenName() + ' ' + oContact.surName()).trim(),
					email,
					addresses = oContact.email();
				if (!oContact.sendToAll()) {
					addresses = addresses.slice(0,1);
				}
				addresses.forEach(address => {
					email = new EmailModel(address.value(), name);
					email.valid() && aE.push(email);
				});
/*
		//		oContact.jCard.getOne('fn')?.notEmpty() ||
				oContact.jCard.parseFullName({set:true});
		//		let name = oContact.jCard.getOne('nickname'),
				let name = oContact.jCard.getOne('fn'),
					email = [oContact.jCard.getOne('email')];
*/
			}
		});

		if (arrayLength(aE)) {
			bOpenCompose = false;
			this.close();
			recipients[sComposeRecipientsField] = aE;
			showMessageComposer([ComposeType.Empty, null, recipients.to, recipients.cc, recipients.bcc])
		}
	}

	clearSearch() {
		this.search('');
	}

	saveCommand() {
		this.saveContact(this.contact());
	}

	saveContact(contact) {
		const data = contact.toJSON();
		if (data.jCard != JSON.stringify(contact.jCard)) {
			this.isSaving(true);
			Remote.request('ContactSave',
				(iError, oData) => {
					if (iError) {
						alert(oData?.message || getNotification(iError));
					} else if (oData.Result.ResultID) {
						if (contact.id()) {
							contact.id(oData.Result.ResultID);
							contact.jCard = JSON.parse(data.jCard);
						} else {
							this.reloadContactList(); // TODO: remove when e-contact-foreach is dynamic
						}
					}
					this.isSaving(false);
				}, data
			);
		}
	}

	syncCommand() {
		ContactUserStore.sync(iError => {
			iError && alert(getNotification(iError));
			this.reloadContactList(true);
		});
	}

	exportVcf() {
		download(serverRequestRaw('ContactsVcf'), 'contacts.vcf');
	}

	exportCsv() {
		download(serverRequestRaw('ContactsCsv'), 'contacts.csv');
	}

	/**
	 * @param {?ContactModel} contact
	 */
	populateViewContact(contact) {
		const oldContact = this.contact(),
			fn = () => this.contact(contact);
		if (oldContact?.hasChanges()) {
			AskPopupView.showModal([
				i18n('GLOBAL/SAVE_CHANGES'),
				() => this.saveContact(oldContact) | fn(),
				fn
			]);
		} else fn();
	}

	reloadContactList() {
		ContactUserStore.loading(true);
		Remote.abort('Contacts').request('Contacts',
			(iError, data) => {
				let count = 0,
					list = [];

				if (iError) {
//					console.error(data);
					alert(data?.message || getNotification(iError));
				} else if (arrayLength(data.Result.List)) {
					data.Result.List.forEach(item => {
						item = ContactModel.reviveFromJson(item);
						item && list.push(item);
					});
					count = pInt(data.Result.Count);
				}

				this.contactsCount(0 < count ? count : 0);

				ContactUserStore(list);

				// Endless scroll: reset the load position for the freshly loaded list
				this.listOffset(list.length);
				this.endReached(ContactUserStore().length >= this.contactsCount());
				this.loadingMore(false);

				ContactUserStore.loading(false);

				setTimeout(() => this.loadMoreIfNeeded(), 50);
			},
			{
				Offset: 0,
				Limit: CONTACTS_PER_PAGE,
				Search: this.search()
			}
		);
	}

	/**
	 * Endless scroll: loads the next chunk of contacts and appends it to the list.
	 */
	loadMore() {
		if (ContactUserStore.loading()
		 || ContactUserStore.importing()
		 || !this.canLoadMore()) {
			return;
		}

		const offset = this.listOffset();

		this.loadingMore(true);
		ContactUserStore.loading(true);

		Remote.abort('Contacts').request('Contacts',
			(iError, data) => {
				this.loadingMore(false);
				ContactUserStore.loading(false);

				if (iError) {
					return;
				}

				const result = data?.Result,
					list = [];

				if (result && arrayLength(result.List)) {
					result.List.forEach(item => {
						item = ContactModel.reviveFromJson(item);
						item && list.push(item);
					});
					this.contactsCount(pInt(result.Count));
				}

				if (!list.length) {
					this.endReached(true);
					return;
				}

				this.listOffset(offset + list.length);
				// Append to the same array so checked/focused states are preserved.
				ContactUserStore.push(...list);

				if (list.length < CONTACTS_PER_PAGE
				 || ContactUserStore().length >= this.contactsCount()) {
					this.endReached(true);
				}

				setTimeout(() => this.loadMoreIfNeeded(), 50);
			},
			{
				Offset: offset,
				Limit: CONTACTS_PER_PAGE,
				Search: this.search()
			}
		);
	}

	/**
	 * Endless scroll: load the next chunk when the list is scrolled near the bottom
	 * or does not fill the viewport yet.
	 */
	loadMoreIfNeeded() {
		const content = this.listContent;
		if (!content || !content.clientHeight || ContactUserStore.loading() || !this.canLoadMore()) {
			return;
		}
		const nearBottom = content.scrollTop + content.clientHeight >= content.scrollHeight - 150,
			notFilled = content.scrollHeight <= content.clientHeight + 5;
		if (nearBottom || notFilled) {
			this.loadMore();
		}
	}

	onBuild(dom) {
		this.listContent = dom.querySelector('.b-list-content');
		this.selector.init(this.listContent, ScopeContacts);

		// Endless scroll: load more contacts when scrolling near the bottom
		this.listContent.addEventListener('scroll', () => this.loadMoreIfNeeded(), { passive: true });

		registerShortcut('delete', '', ScopeContacts, () => {
			this.deleteCommand();
			return false;
		});

		registerShortcut('c,w', '', ScopeContacts, () => {
			this.newMessageCommand();
			return false;
		});

		// initUploader

		if (this.importButton()) {
			const j = new Jua({
				action: serverRequest('UploadContacts'),
				limit: 1,
				clickElement: this.importButton()
			});

			if (j) {
				j.on('onStart', () => {
					ContactUserStore.importing(true);
				}).on('onComplete', (id, result, data) => {
					ContactUserStore.importing(false);
					this.reloadContactList();
					if (!id || !result || !data || !data.Result) {
						alert(i18n('CONTACTS/ERROR_IMPORT_FILE'));
					}
				});
			}
		}
	}

	onClose() {
		const contact = this.contact();
		if (AskPopupView.hidden() && contact?.hasChanges()) {
			AskPopupView.showModal([
				i18n('GLOBAL/SAVE_CHANGES'),
				() => this.close() | this.saveContact(contact),
				() => this.close()
			]);
			return false;
		}
	}

	onShow(bBackToCompose, sRecipientsField) {
		bOpenCompose = !!bBackToCompose;
		sComposeRecipientsField = ['to','cc','bcc'].includes(sRecipientsField) ? sRecipientsField : 'to';
		this.reloadContactList(true);
	}

	onHide() {
		this.contact(null);
		this.selectorContact(null);
		this.search('');
		this.contactsCount(0);

		// Endless scroll: reset state for the next time the dialog opens
		this.listOffset(0);
		this.endReached(false);
		this.loadingMore(false);

		ContactUserStore([]);

		bOpenCompose && showMessageComposer();
	}
}
