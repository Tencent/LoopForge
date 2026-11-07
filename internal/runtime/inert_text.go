package runtime

import (
	"strings"
	"unicode"
)

// cliBreak is a zero-width space. It stops the CodeBuddy CLI from acting on
// the text after it while leaving what the model reads unchanged.
const cliBreak = "​"

// InertCLIText disarms what the CodeBuddy CLI executes when it renders a
// skill (Skill tool, subagent preload) on the host it runs on:
//
//   - !`cmd` runs cmd in a shell, anywhere in the text, code blocks included;
//   - @path outside code inlines that file or directory listing, and ~/ or an
//     absolute path reaches anywhere on the host.
//
// A conversation CLI runs on the control plane with Bash disabled, so skill
// text written for it must not reach either. The @ rule mirrors the CLI's own
// (an @ after whitespace or at the start, outside ``` blocks and `code`), so
// code samples such as `@types/node` are left as they are.
func InertCLIText(text string) string {
	text = strings.ReplaceAll(text, "!`", "!"+cliBreak+"`")
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(text)
	fences, ticks := 0, 0
	for i, r := range runes {
		if r == '@' && (i == 0 || unicode.IsSpace(runes[i-1])) && i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) && runes[i+1] != '@' && fences%2 == 0 && ticks%2 == 0 {
			b.WriteString(cliBreak)
		}
		b.WriteRune(r)
		if r == '`' {
			if i+2 < len(runes) && runes[i+1] == '`' && runes[i+2] == '`' && (i == 0 || runes[i-1] != '`') {
				fences++
			}
			if (i == 0 || runes[i-1] != '`') && (i+1 >= len(runes) || runes[i+1] != '`') {
				ticks++
			}
		}
	}
	return b.String()
}
