package upgrade

import "strings"

// ShellQuote renders one value as a single POSIX shell word. Go quoting is not
// shell syntax: a double-quoted path still expands `$`, backticks and `\`, so a
// printed command an operator pastes would address a different file. Single
// quotes stop every expansion; an embedded single quote is written as the
// standard close-escape-reopen sequence.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
