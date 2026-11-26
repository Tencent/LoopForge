package domain

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxResourceIDLength matches the id columns of the catalog tables (varchar(64)).
const MaxResourceIDLength = 64

// resourceIDReserved are characters a URL path segment cannot carry literally.
// A project or role id is a segment of its own API path (/api/projects/:id,
// /api/agents/:id), so an id holding one of them creates a record that can no
// longer be opened, edited or deleted.
const resourceIDReserved = `/\?#%`

// ValidateResourceID checks an identifier chosen for a new project or role.
// Existing records are not re-validated, so older ids stay editable.
func ValidateResourceID(id string) error {
	if id == "" {
		return errors.New("标识不能为空")
	}
	if utf8.RuneCountInString(id) > MaxResourceIDLength {
		return errors.New("标识不能超过 64 个字符")
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(resourceIDReserved, r) {
			return errors.New("标识不能包含空格或 / \\ ? # % 字符")
		}
	}
	return nil
}
