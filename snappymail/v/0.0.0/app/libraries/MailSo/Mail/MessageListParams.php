<?php

/*
 * This file is part of MailSo.
 *
 * (c) 2014 Usenko Timur
 *
 * For the full copyright and license information, please view the LICENSE
 * file that was distributed with this source code.
 */

namespace MailSo\Mail;

class MessageListParams
{
	public string
		$sFolderName,
		$sSearch = '',
		$sSort = '',
		$sThreadAlgorithm = '';

	public ?\MailSo\Cache\CacheClient
		$oCacher = null;

	public bool
		$bUseSort = true,
		$bUseThreads = false,
		$bHideDeleted = true,
		$bSearchFuzzy = false;

	protected int
		$iOffset = 0,
		$iLimit = 0,
		$iPrevUidNext = 0, // used to check for new messages
		$iThreadUid = 0;

	/**
	 * Messages with message sequence numbers corresponding to the specified message sequence number set.
	 */
	public ?\MailSo\Imap\SequenceSet $oSequenceSet = null;

	public function __get($k)
	{
		return \property_exists($this, $k) ? $this->$k : null;
	}

	public function __set($k, $v)
	{
		if ('i' === $k[0]) {
			$this->$k = \max(0, (int) $v);
		}
//		0 > $oParams->iOffset
//		0 > $oParams->iLimit
//		999 < $oParams->iLimit
	}

	public function hash() : string
	{
		return \md5(\implode('-', [
			$this->sFolderName,
			$this->iOffset,
			$this->iLimit,
			$this->bHideDeleted ? '1' : '0',
			$this->sSearch,
			$this->bSearchFuzzy ? '1' : '0',
			$this->bUseSort ? $this->sSort : '0',
			$this->bUseThreads ? $this->iThreadUid : '',
			$this->bUseThreads ? $this->sThreadAlgorithm : '',
//			$this->oSequenceSet ? $this->oSequenceSet : '',
			$this->iPrevUidNext
		]));
	}

	/**
	 * Strict whitelist validation of $sSort, so no user input can be
	 * injected into the IMAP SORT command (see \MailSo\Imap\Enumerations\SortCriteria).
	 *
	 *	(REVERSE\s+)?(ARRIVAL|CC|DATE|FROM|SIZE|SUBJECT|TO|DISPLAYFROM|DISPLAYTO)
	 *		ARRIVAL
	 *			Internal date and time of the message.  This differs from the
	 *			ON criteria in SEARCH, which uses just the internal date.
	 *
	 *		CC
	 *			[IMAP] addr-mailbox of the first "cc" address.
	 *
	 *		DATE
	 *			Sent date and time, as described in section 2.2.
	 *
	 *		FROM
	 *			[IMAP] addr-mailbox of the first "From" address.
	 *
	 *		REVERSE
	 *			Followed by another sort criterion, has the effect of that
	 *			criterion but in reverse (descending) order.
	 *			Note: REVERSE only reverses a single criterion, and does not
	 *			affect the implicit "sequence number" sort criterion if all
	 *			other criteria are identical.  Consequently, a sort of
	 *			REVERSE SUBJECT is not the same as a reverse ordering of a
	 *			SUBJECT sort.  This can be avoided by use of additional
	 *			criteria, e.g., SUBJECT DATE vs. REVERSE SUBJECT REVERSE
	 *			DATE.  In general, however, it's better (and faster, if the
	 *			client has a "reverse current ordering" command) to reverse
	 *			the results in the client instead of issuing a new SORT.
	 *
	 *		SIZE
	 *			Size of the message in octets.
	 *
	 *		SUBJECT
	 *			Base subject text.
	 *
	 *		TO
	 *			[IMAP] addr-mailbox of the first "To" address.
	 *
	 *		RFC 5957:
	 *			$oImapClient->hasCapability('SORT=DISPLAY')
	 *			DISPLAYFROM, DISPLAYTO
	 */
	public function sortValid(?\MailSo\Imap\ImapClient $oImapClient = null) : bool
	{
		return \MailSo\Imap\Enumerations\SortCriteria::validate(
			$this->sSort,
			$oImapClient && $oImapClient->hasCapability('SORT=DISPLAY')
		);
	}

	/**
	 * The validated $sSort rebuilt from the SortCriteria enum,
	 * or an empty string when $sSort is empty or invalid.
	 *
	 * Only this value may be sent to the IMAP server.
	 */
	public function sortCriteria(?\MailSo\Imap\ImapClient $oImapClient = null) : string
	{
		return \MailSo\Imap\Enumerations\SortCriteria::normalize(
			$this->sSort,
			$oImapClient && $oImapClient->hasCapability('SORT=DISPLAY')
		);
	}

}
