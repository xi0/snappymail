package mime

// The Email/EmailCollection models of MailSo\Mime\Email and
// MailSo\Mime\EmailCollection, including the address parsing the message list
// uses to build "from"/"to"/"cc"/"bcc" fields.
//
// The JSON shapes are the frontend contract: an Email is
// {"@Object":"Object/Email","name","email","dkimStatus"}, and an
// EmailCollection serializes to the plain list of its Emails (the PHP
// EmailCollection::jsonSerialize() returns array_slice($this->getArrayCopy())).

import "strings"

// Email is MailSo\Mime\Email.
type Email struct {
	email       string
	displayName string
	dkimStatus  string
}

// NewEmail is Email::__construct().
func NewEmail(address, displayName string) *Email {
	return &Email{
		email:       strings.TrimSpace(address),
		displayName: Trim(displayName),
		dkimStatus:  DkimStatusNone,
	}
}

// GetEmail is Email::GetEmail().
func (e *Email) GetEmail() string {
	if nil == e {
		return ""
	}
	return e.email
}

// GetDisplayName is Email::GetDisplayName().
func (e *Email) GetDisplayName() string {
	if nil == e {
		return ""
	}
	return e.displayName
}

// GetDomain is Email::GetDomain().
func (e *Email) GetDomain() string {
	address := e.GetEmail()
	if at := strings.LastIndexByte(address, '@'); 0 <= at {
		return address[at+1:]
	}
	return ""
}

// ToString is Email::ToString(): the address, optionally with its display name
// and RFC 2047 encoded display name (the header writer of Step 19).
func (e *Email) ToString(convertSpecialsName, utf8 bool) string {
	if nil == e || "" == e.email {
		return ""
	}

	// Email::GetEmail($bUtf8) converts the domain to unicode. The builder only
	// renders the ASCII form (Header::__toString() calls ToString(true)), so the
	// conversion is not needed here.
	_ = utf8
	result := e.email

	displayName := e.displayName
	if "" != displayName {
		displayName = strings.ReplaceAll(displayName, `"`, `\"`)
		if convertSpecialsName {
			displayName = EncodeHeaderValue(displayName)
		}
		result = `"` + displayName + `" <` + result + `>`
	}
	return result
}

// SetDkimStatus is Email::SetDkimStatus().
func (e *Email) SetDkimStatus(status string) {
	if nil == e {
		return
	}
	if "" == status {
		status = DkimStatusNone
	}
	e.dkimStatus = status
}

// JSONSerialize is Email::jsonSerialize().
func (e *Email) JSONSerialize() any {
	return map[string]any{
		"@Object":    "Object/Email",
		"name":       Utf8Clear(e.displayName),
		"email":      Utf8Clear(e.email),
		"dkimStatus": e.dkimStatus,
	}
}

// ParseEmail is Email::Parse(): split a single "Name <addr>" / comment address.
func ParseEmail(address string) *Email {
	address = Trim(DecodeHeaderValue(address, ""))
	if "" == strings.TrimSpace(address) {
		return nil
	}

	name := ""
	mail := ""
	comment := ""

	inName := false
	inAddress := false
	inComment := false
	start := 0
	index := 0

	for index < len(address) {
		switch address[index] {
		case '"':
			if !inName && !inAddress && !inComment {
				inName = true
				start = index
			} else if !inAddress && !inComment {
				name = address[start+1 : index]
				address = address[:start] + address[index+1:]
				index = 0
				start = 0
				inName = false
			}
		case '<':
			if !inName && !inAddress && !inComment {
				if 0 < index && "" == name {
					name = address[:index]
				}
				inAddress = true
				start = index
			}
		case '>':
			if inAddress {
				mail = address[start+1 : index]
				address = address[:start] + address[index+1:]
				index = 0
				start = 0
				inAddress = false
			}
		case '(':
			if !inName && !inAddress && !inComment {
				inComment = true
				start = index
			}
		case ')':
			if inComment {
				comment = address[start+1 : index]
				address = address[:start] + address[index+1:]
				index = 0
				start = 0
				inComment = false
			}
		case '\\':
			index++
		}
		index++
	}

	if "" == mail {
		if match := mustRegexp(`[^@\s]+@\S+`).FindString(address); "" != match {
			mail = match
		} else {
			name = address
		}
	}

	if "" != mail && "" == name && "" == comment {
		name = strings.ReplaceAll(address, mail, "")
	}

	mail = strings.Trim(strings.TrimSpace(mail), "<>")
	mail = strings.TrimRight(strings.TrimSpace(mail), ".")
	mail = strings.TrimSpace(mail)

	name = strings.Trim(strings.Trim(name, " "), `"`)
	name = strings.Trim(name, "'")
	comment = strings.Trim(strings.Trim(comment, " "), "()")

	name = mustRegexp(`\\(.)`).ReplaceAllString(name, "$1")
	comment = mustRegexp(`\\(.)`).ReplaceAllString(comment, "$1")

	if "" == name && "" != comment {
		name = comment
	}
	if "" == mail && "" == name {
		return nil
	}
	return NewEmail(mail, name)
}

// EmailCollection is MailSo\Mime\EmailCollection.
type EmailCollection struct {
	items []*Email
}

// NewEmailCollection is EmailCollection::__construct().
func NewEmailCollection(raw string) *EmailCollection {
	collection := &EmailCollection{}
	collection.parseEmailAddresses(raw)
	return collection
}

// Count is Collection::count().
func (c *EmailCollection) Count() int {
	if nil == c {
		return 0
	}
	return len(c.items)
}

// Iterate returns the emails.
func (c *EmailCollection) Iterate() []*Email {
	if nil == c {
		return nil
	}
	return c.items
}

// Append is EmailCollection::append().
func (c *EmailCollection) Append(email *Email) {
	if nil == c || nil == email {
		return
	}
	c.items = append(c.items, email)
}

// Unique is EmailCollection::Unique(): de-duplicate by e-mail address, keeping
// the first occurrence order.
func (c *EmailCollection) Unique() *EmailCollection {
	result := &EmailCollection{}
	if nil == c {
		return result
	}

	seen := make(map[string]struct{}, len(c.items))
	for _, email := range c.items {
		address := email.GetEmail()
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		result.items = append(result.items, email)
	}
	return result
}

// ToString is EmailCollection::ToString().
func (c *EmailCollection) ToString(convertSpecialsName, utf8 bool) string {
	if nil == c {
		return ""
	}
	items := make([]string, 0, len(c.items))
	for _, email := range c.items {
		items = append(items, email.ToString(convertSpecialsName, utf8))
	}
	return strings.Join(items, ", ")
}

// parseEmailAddresses is EmailCollection::parseEmailAddresses().
func (c *EmailCollection) parseEmailAddresses(raw string) {
	raw = strings.TrimSpace(raw)
	if "" == raw {
		return
	}

	start := 0
	inQuotes := false
	quote := byte('"')
	inAngle := false
	inBracket := false

	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '\'', '"':
			if !inQuotes {
				quote = raw[index]
				inQuotes = true
			} else if quote == raw[index] {
				inQuotes = false
			}
		case '<':
			if !inAngle {
				inAngle = true
				if inQuotes {
					inQuotes = false
				}
			}
		case '>':
			if inAngle {
				inAngle = false
			}
		case '(':
			if !inBracket {
				inBracket = true
			}
		case ')':
			if inBracket {
				inBracket = false
			}
		case ',', ';':
			if !inAngle && !inBracket && !inQuotes {
				if email := ParseEmail(raw[start:index]); nil != email {
					c.Append(email)
				}
				start = index + 1
			}
		}
	}

	if start < len(raw) {
		if email := ParseEmail(raw[start:]); nil != email {
			c.Append(email)
		}
	}
}

// JSONSerialize is EmailCollection::jsonSerialize(): a plain list capped at 100.
func (c *EmailCollection) JSONSerialize() any {
	limit := c.Count()
	if 100 < limit {
		limit = 100
	}
	result := make([]any, 0, limit)
	for _, email := range c.items[:limit] {
		result = append(result, email)
	}
	return result
}
