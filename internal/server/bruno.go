package server

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
)

// Bruno (.bru) is the on-disk request format of the Bruno API client
// (https://www.usebruno.com). We support a deliberately small subset: the
// method/url verb block, the headers block and the body block. Full Bruno
// features (auth blocks, vars, scripting, assertions) are ignored on read and
// never emitted on write.

// message is the portable shape shared by download (encode) and
// upload (decode): what a webhook request carried.
type message struct {
	Method  string
	Path    string // request URI, informational; forwarding uses the configured destination
	Headers http.Header
	Body    string
}

// verbs are the HTTP methods Bruno models as named request blocks.
var verbs = []string{"get", "post", "put", "delete", "patch", "options", "head"}

// encodeBruno renders a message as a .bru document. url is written into the
// verb block so the file re-imports into Bruno pointing at the same place.
func encodeBruno(name, url string, m message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "meta {\n  name: %s\n  type: http\n  seq: 1\n}\n\n", brunoSanitize(name))

	verb := strings.ToLower(m.Method)
	if verb == "" {
		verb = "post"
	}
	bodyType := brunoBodyType(m)
	fmt.Fprintf(&b, "%s {\n  url: %s\n  body: %s\n  auth: none\n}\n\n", verb, brunoSanitize(url), bodyType)

	if names := sortedHeaderNames(m.Headers); len(names) > 0 {
		b.WriteString("headers {\n")
		for _, name := range names {
			for _, v := range m.Headers[name] {
				fmt.Fprintf(&b, "  %s: %s\n", name, brunoSanitize(v))
			}
		}
		b.WriteString("}\n\n")
	}

	if m.Body != "" {
		// Bruno indents multi-line block bodies by two spaces; the reader
		// tolerates either, so we keep the payload verbatim for readability.
		fmt.Fprintf(&b, "body:%s {\n%s\n}\n", bodyType, m.Body)
	}
	return b.String()
}

func brunoBodyType(m message) string {
	for _, v := range m.Headers["Content-Type"] {
		if strings.Contains(strings.ToLower(v), "json") {
			return "json"
		}
	}
	return "text"
}

// brunoSanitize keeps single-line values single-line; newlines would break the
// key: value grammar. Bruno itself has no escaping for these, so we replace.
func brunoSanitize(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func sortedHeaderNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// decodeBruno parses the subset we emit (and hand-written equivalents) back
// into a message. It extracts the method from the verb block, the headers
// block and the body block; everything else is ignored. A document with no
// recognizable verb block is rejected.
func decodeBruno(src string) (message, error) {
	m := message{Headers: http.Header{}}
	blocks := brunoBlocks(src)

	foundVerb := false
	for _, blk := range blocks {
		switch {
		case isVerb(blk.name):
			foundVerb = true
			m.Method = strings.ToUpper(blk.name)
			m.Path = brunoField(blk.body, "url")
		case blk.name == "headers":
			for _, line := range strings.Split(blk.body, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				m.Headers.Add(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		case blk.name == "body" || strings.HasPrefix(blk.name, "body:"):
			// Block bodies are commonly indented two spaces; trim a uniform
			// leading indent but preserve internal structure.
			m.Body = dedent(strings.Trim(blk.body, "\n"))
		}
	}
	if !foundVerb {
		return message{}, fmt.Errorf("no HTTP method block found (expected e.g. %q)", "post { ... }")
	}
	if m.Method == "" {
		m.Method = http.MethodPost
	}
	return m, nil
}

type brunoBlock struct {
	name string
	body string
}

// brunoBlocks splits a .bru document into top-level "name { ... }" blocks,
// honoring brace nesting so a JSON body's braces don't end the block early.
func brunoBlocks(src string) []brunoBlock {
	var blocks []brunoBlock
	i, n := 0, len(src)
	for i < n {
		// Read a block header: everything up to the first '{' on/after a name.
		open := strings.IndexByte(src[i:], '{')
		if open < 0 {
			break
		}
		name := strings.TrimSpace(src[i : i+open])
		if name == "" {
			i += open + 1
			continue
		}
		// Find the matching close brace from just after this open brace.
		depth := 1
		j := i + open + 1
		for ; j < n; j++ {
			switch src[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			break // unterminated block
		}
		blocks = append(blocks, brunoBlock{name: name, body: src[i+open+1 : j]})
		i = j + 1
	}
	return blocks
}

// brunoField returns the value of a "key: value" line within a block body.
func brunoField(body, key string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func isVerb(name string) bool {
	return slices.Contains(verbs, name)
}

// dedent removes the smallest common leading-whitespace indent from every
// non-blank line, undoing Bruno's block-body indentation.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	indent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent <= 0 {
		return s
	}
	for i, line := range lines {
		if len(line) >= indent {
			lines[i] = line[indent:]
		}
	}
	return strings.Join(lines, "\n")
}
