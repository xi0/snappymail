<?php

/*
 * This file is part of MailSo.
 *
 * (c) 2014 Usenko Timur
 *
 * For the full copyright and license information, please view the LICENSE
 * file that was distributed with this source code.
 *
 * https://datatracker.ietf.org/doc/html/rfc5256
 * https://datatracker.ietf.org/doc/html/rfc5957
 */

namespace MailSo\Imap\Enumerations;

/**
 * Strict whitelist of the SORT criteria that may be sent to the IMAP server.
 *
 * The sort criteria are placed unquoted inside a parenthesized list in the
 * SORT/ESORT command, so any value that isn't one of these constants would
 * allow IMAP command injection (e.g. "DATE) (UID 1:2").
 *
 * @category MailSo
 * @package Imap
 * @subpackage Enumerations
 */
abstract class SortCriteria
{
	const
		ARRIVAL = 'ARRIVAL',
		CC = 'CC',
		DATE = 'DATE',
		FROM = 'FROM',
		SIZE = 'SIZE',
		SUBJECT = 'SUBJECT',
		TO = 'TO',
		// RFC 5957, only when the server has capability SORT=DISPLAY
		DISPLAYFROM = 'DISPLAYFROM',
		DISPLAYTO = 'DISPLAYTO',
		// RFC 5256 4.
		REVERSE = 'REVERSE';

	// RFC 5256
	const DEFAULT_CRITERIA = [
		self::ARRIVAL,
		self::CC,
		self::DATE,
		self::FROM,
		self::SIZE,
		self::SUBJECT,
		self::TO
	];

	// RFC 5957
	const DISPLAY_CRITERIA = [
		self::DISPLAYFROM,
		self::DISPLAYTO
	];

	/**
	 * Split a SORT criteria string into its (uppercased) tokens.
	 */
	private static function tokenize(string $sSort) : array
	{
		return \preg_split('/\s+/', \strtoupper(\trim($sSort)), -1, PREG_SPLIT_NO_EMPTY) ?: [];
	}

	/**
	 * Strict whitelist validation of a SORT criteria string like "REVERSE SUBJECT DATE".
	 * Returns true for an empty string (no explicit sort criteria).
	 */
	public static function validate(string $sSort, bool $bDisplay = false) : bool
	{
		$aAllowed = $bDisplay ? \array_merge(self::DEFAULT_CRITERIA, self::DISPLAY_CRITERIA) : self::DEFAULT_CRITERIA;
		$bReverse = false;
		foreach (self::tokenize($sSort) as $sCriterion) {
			if (self::REVERSE === $sCriterion) {
				// REVERSE must be followed by a criterion, "REVERSE REVERSE" is invalid
				if ($bReverse) {
					return false;
				}
				$bReverse = true;
			} else if (\in_array($sCriterion, $aAllowed, true)) {
				$bReverse = false;
			} else {
				return false;
			}
		}
		// A trailing REVERSE has no criterion to reverse
		return !$bReverse;
	}

	/**
	 * Returns the validated SORT criteria rebuilt from the enum constants,
	 * or an empty string when $sSort is empty or invalid.
	 *
	 * This guarantees that only whitelisted values end up in the IMAP command.
	 */
	public static function normalize(string $sSort, bool $bDisplay = false) : string
	{
		if (!self::validate($sSort, $bDisplay)) {
			return '';
		}
		return \implode(' ', self::tokenize($sSort));
	}
}
