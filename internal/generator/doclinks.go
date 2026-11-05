package main

import (
	"regexp"
	"strings"
)

// toolName is the MCP tool name of an operation: its path with `/` and `-` turned into `_`. The
// spec descriptions are shared with MCP and name operations this way (`tags_insights`).
func toolName(path string) string {
	return strings.ReplaceAll(strings.Join(strings.Split(strings.Trim(path, "/"), "/"), "_"), "-", "_")
}

var toolToken = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)*\b`)

// linkToolNames rewrites the tool names in one description into doc links to the methods that
// run them; links maps a tool name to its link, or to "" when two operations share it. Only whole
// tokens that are tool names change, so event names and enum values such as `purchase_approved`
// stay as written. A token alone in its backticks loses them and becomes the link; inside a larger
// code span only the token changes; in prose a snake_case one changes and a single word does not,
// since a root method name such as whoami is also an ordinary word. Spans are tracked per line,
// as descriptions do not break code spans across lines.
func linkToolNames(text string, links map[string]string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = linkLine(line, links)
	}
	return strings.Join(lines, "\n")
}

func linkLine(line string, links map[string]string) string {
	var b strings.Builder
	last := 0
	for _, m := range toolToken.FindAllStringIndex(line, -1) {
		start, end := m[0], m[1]
		token := line[start:end]
		link := links[token]
		if link == "" {
			continue
		}
		quoted := start > 0 && end < len(line) && line[start-1] == '`' && line[end] == '`'
		inSpan := strings.Count(line[:start], "`")%2 == 1
		switch {
		case quoted && inSpan:
			start, end = start-1, end+1
		case quoted || inSpan:
		case !strings.Contains(token, "_"):
			continue
		}
		b.WriteString(line[last:start])
		b.WriteString(link)
		last = end
	}
	b.WriteString(line[last:])
	return b.String()
}
