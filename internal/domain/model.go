package domain

import "strings"

// ChosenModel returns the model a setting names, or "" when it names none.
// A setting without a model runs on the first model its picker lists.
// Settings saved by earlier versions may hold "default", the retired
// placeholder for "let the CLI pick"; it reads as no model chosen.
func ChosenModel(id string) string {
	id = strings.TrimSpace(id)
	if id == "default" {
		return ""
	}
	return id
}
