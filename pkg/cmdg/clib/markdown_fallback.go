// Package clib provides C-based performance-critical operations.
package clib

import "html"

func markdownPlainTextHTML(md []byte) []byte {
	return []byte("<pre>" + html.EscapeString(string(md)) + "</pre>")
}
