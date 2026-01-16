package notion

import (
	"fmt"
	"strings"
)

// MarkdownToBlocks converts markdown text to Notion block structures.
func MarkdownToBlocks(markdown string) []map[string]any {
	var blocks []map[string]any
	lines := splitLines(markdown)

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if line == "" {
			continue
		}

		// Skip child_page comment markers - these are placeholders, actual child pages are restored separately
		if strings.HasPrefix(line, "<!-- child_page:") && strings.HasSuffix(line, "-->") {
			continue
		}

		// Heading 1
		if len(line) > 2 && line[0] == '#' && line[1] == ' ' {
			blocks = append(blocks, map[string]any{
				"object": "block",
				"type":   "heading_1",
				"heading_1": map[string]any{
					"rich_text": parseInlineMarkdown(line[2:]),
				},
			})
			continue
		}

		// Heading 2
		if len(line) > 3 && line[0:3] == "## " {
			blocks = append(blocks, map[string]any{
				"object": "block",
				"type":   "heading_2",
				"heading_2": map[string]any{
					"rich_text": parseInlineMarkdown(line[3:]),
				},
			})
			continue
		}

		// Heading 3
		if len(line) > 4 && line[0:4] == "### " {
			blocks = append(blocks, map[string]any{
				"object": "block",
				"type":   "heading_3",
				"heading_3": map[string]any{
					"rich_text": parseInlineMarkdown(line[4:]),
				},
			})
			continue
		}

		// Divider
		if line == "---" {
			blocks = append(blocks, map[string]any{
				"object":  "block",
				"type":    "divider",
				"divider": map[string]any{},
			})
			continue
		}

		// Code block (fenced with ```)
		if len(line) >= 3 && line[0:3] == "```" {
			lang := strings.TrimSpace(line[3:])
			var codeLines []string
			// Collect lines until closing ```
			for i+1 < len(lines) {
				i++
				if len(lines[i]) >= 3 && lines[i][0:3] == "```" {
					break
				}
				codeLines = append(codeLines, lines[i])
			}
			codeContent := strings.Join(codeLines, "\n")
			// Map common language aliases to Notion's expected values
			notionLang := mapLanguageToNotion(lang)
			blocks = append(blocks, map[string]any{
				"object": "block",
				"type":   "code",
				"code": map[string]any{
					"rich_text": []map[string]any{
						{"type": "text", "text": map[string]string{"content": codeContent}},
					},
					"language": notionLang,
				},
			})
			continue
		}

		// Checkbox (to_do)
		if len(line) > 5 && (line[0:5] == "- [ ]" || line[0:5] == "- [x]") {
			checked := line[3] == 'x'
			text := ""
			if len(line) > 6 {
				text = line[6:]
			}
			blocks = append(blocks, map[string]any{
				"object": "block",
				"type":   "to_do",
				"to_do": map[string]any{
					"rich_text": parseInlineMarkdown(text),
					"checked":   checked,
				},
			})
			continue
		}

		// Bullet list
		if len(line) > 2 && line[0:2] == "- " {
			blocks = append(blocks, map[string]any{
				"object": "block",
				"type":   "bulleted_list_item",
				"bulleted_list_item": map[string]any{
					"rich_text": parseInlineMarkdown(line[2:]),
				},
			})
			continue
		}

		// Numbered list
		if len(line) > 3 && line[0] >= '0' && line[0] <= '9' {
			dotIdx := -1
			for j := 1; j < len(line) && j < 4; j++ {
				if line[j] == '.' && j+1 < len(line) && line[j+1] == ' ' {
					dotIdx = j
					break
				}
				if line[j] < '0' || line[j] > '9' {
					break
				}
			}
			if dotIdx > 0 {
				blocks = append(blocks, map[string]any{
					"object": "block",
					"type":   "numbered_list_item",
					"numbered_list_item": map[string]any{
						"rich_text": parseInlineMarkdown(line[dotIdx+2:]),
					},
				})
				continue
			}
		}

		// Quote
		if len(line) > 2 && line[0:2] == "> " {
			blocks = append(blocks, map[string]any{
				"object": "block",
				"type":   "quote",
				"quote": map[string]any{
					"rich_text": parseInlineMarkdown(line[2:]),
				},
			})
			continue
		}

		// Table
		if len(line) > 0 && line[0] == '|' {
			tableRows := []string{line}
			for i+1 < len(lines) && len(lines[i+1]) > 0 && lines[i+1][0] == '|' {
				i++
				tableRows = append(tableRows, lines[i])
			}
			tableBlock := parseMarkdownTable(tableRows)
			if tableBlock != nil {
				blocks = append(blocks, tableBlock)
			}
			continue
		}

		// Default: paragraph
		blocks = append(blocks, map[string]any{
			"object": "block",
			"type":   "paragraph",
			"paragraph": map[string]any{
				"rich_text": parseInlineMarkdown(line),
			},
		})
	}

	return blocks
}

// BlocksToMarkdown converts Notion API block results to markdown.
// This is the legacy function - use BlocksToMarkdownWithChildPages for proper child page handling.
func BlocksToMarkdown(blocks []map[string]any) string {
	return BlocksToMarkdownWithChildPages(blocks, nil)
}

// BlocksToMarkdownWithChildPages converts Notion API block results to markdown.
// Child pages that are not trailing become mentions [@Title](notion://ID).
// Trailing child pages (those after the last real content) are skipped entirely
// since they'll be restored at the bottom anyway.
func BlocksToMarkdownWithChildPages(blocks []map[string]any, trailingChildPages map[string]bool) string {
	var result strings.Builder
	listNum := 1
	lastType := ""

	for _, b := range blocks {
		blockType, _ := b["type"].(string)
		if blockType == "" {
			continue
		}

		if blockType != "numbered_list_item" && lastType == "numbered_list_item" {
			listNum = 1
		}

		text := extractBlockText(b, blockType)

		switch blockType {
		case "heading_1":
			result.WriteString("# " + text + "\n\n")
		case "heading_2":
			result.WriteString("## " + text + "\n\n")
		case "heading_3":
			result.WriteString("### " + text + "\n\n")
		case "paragraph":
			result.WriteString(text + "\n\n")
		case "bulleted_list_item":
			result.WriteString("- " + text + "\n")
		case "numbered_list_item":
			result.WriteString(fmt.Sprintf("%d. %s\n", listNum, text))
			listNum++
		case "to_do":
			check := " "
			if todoBlock, ok := b["to_do"].(map[string]any); ok {
				if checked, ok := todoBlock["checked"].(bool); ok && checked {
					check = "x"
				}
			}
			result.WriteString(fmt.Sprintf("- [%s] %s\n", check, text))
		case "quote":
			result.WriteString("> " + text + "\n\n")
		case "callout":
			result.WriteString("> " + text + "\n\n")
		case "code":
			lang := ""
			if codeBlock, ok := b["code"].(map[string]any); ok {
				if l, ok := codeBlock["language"].(string); ok {
					lang = l
				}
			}
			result.WriteString("```" + lang + "\n" + text + "\n```\n\n")
		case "divider":
			result.WriteString("---\n\n")
		case "child_page":
			// Handle child pages as mentions (links to the page)
			// Trailing child pages are skipped - they'll be at the bottom after restore anyway
			pageID, _ := b["id"].(string)
			if pageID != "" {
				// Skip trailing child pages entirely
				if trailingChildPages != nil && trailingChildPages[pageID] {
					// Don't output anything - will be restored at bottom
					continue
				}
				// Non-trailing: output as mention link
				if childPage, ok := b["child_page"].(map[string]any); ok {
					title, _ := childPage["title"].(string)
					if title == "" {
						title = "Untitled"
					}
					// Output as mention: [@Title](notion://page-id)
					result.WriteString(fmt.Sprintf("[@%s](notion://%s)\n\n", title, pageID))
				}
			}
		case "table":
			result.WriteString(extractTableMarkdown(b) + "\n")
			if tableComments := extractTableComments(b); tableComments != "" {
				result.WriteString(tableComments)
			}
		default:
			if text != "" {
				result.WriteString(text + "\n\n")
			}
		}

		if blockType != "table" {
			if commentStr := formatBlockComments(b); commentStr != "" {
				result.WriteString(commentStr)
			}
		}

		lastType = blockType
	}

	return result.String()
}

func formatBlockComments(b map[string]any) string {
	comments, ok := b["_comments"].([]map[string]any)
	if !ok || len(comments) == 0 {
		return ""
	}

	var result strings.Builder
	for _, c := range comments {
		author, _ := c["author"].(string)
		content, _ := c["content"].(string)
		date, _ := c["created_at"].(string)
		if author == "" {
			author = "Unknown"
		}
		result.WriteString(fmt.Sprintf("> **%s** *(%s)*: %s\n\n", author, date, content))
	}
	return result.String()
}

func extractTableComments(b map[string]any) string {
	tableData, ok := b["table"].(map[string]any)
	if !ok {
		return ""
	}

	children, ok := tableData["children"].([]any)
	if !ok {
		return ""
	}

	var result strings.Builder
	for _, child := range children {
		if row, ok := child.(map[string]any); ok {
			if comments, ok := row["_comments"].([]map[string]any); ok && len(comments) > 0 {
				for _, c := range comments {
					author, _ := c["author"].(string)
					content, _ := c["content"].(string)
					date, _ := c["created_at"].(string)
					if author == "" {
						author = "Unknown"
					}
					result.WriteString(fmt.Sprintf("> **%s** *(%s)*: %s\n\n", author, date, content))
				}
			}
		}
	}
	return result.String()
}

func extractBlockText(b map[string]any, blockType string) string {
	var richText []any

	if content, ok := b[blockType].(map[string]any); ok {
		if rt, ok := content["rich_text"].([]any); ok {
			richText = rt
		}
	}

	return richTextToMarkdown(richText)
}

// richTextToMarkdown converts Notion rich_text array to markdown string,
// preserving formatting, links, and mentions.
func richTextToMarkdown(richText []any) string {
	var text strings.Builder
	for _, item := range richText {
		rt, ok := item.(map[string]any)
		if !ok {
			continue
		}

		itemType, _ := rt["type"].(string)
		content := ""
		var linkURL string

		switch itemType {
		case "text":
			if textObj, ok := rt["text"].(map[string]any); ok {
				content, _ = textObj["content"].(string)
				// Check for link
				if link, ok := textObj["link"].(map[string]any); ok {
					linkURL, _ = link["url"].(string)
				}
			}
		case "mention":
			if mention, ok := rt["mention"].(map[string]any); ok {
				mentionType, _ := mention["type"].(string)
				switch mentionType {
				case "page":
					if page, ok := mention["page"].(map[string]any); ok {
						pageID, _ := page["id"].(string)
						plainText, _ := rt["plain_text"].(string)
						if plainText == "" {
							plainText = "Page"
						}
						// Format as Notion page mention: [@Page Title](notion://page-id)
						text.WriteString(fmt.Sprintf("[@%s](notion://%s)", plainText, pageID))
						continue
					}
				case "user":
					plainText, _ := rt["plain_text"].(string)
					text.WriteString(plainText)
					continue
				case "date":
					plainText, _ := rt["plain_text"].(string)
					text.WriteString(plainText)
					continue
				default:
					plainText, _ := rt["plain_text"].(string)
					text.WriteString(plainText)
					continue
				}
			}
			// Fallback to plain_text
			if pt, ok := rt["plain_text"].(string); ok {
				content = pt
			}
		default:
			// Fallback to plain_text for unknown types
			if pt, ok := rt["plain_text"].(string); ok {
				content = pt
			}
		}

		// Apply annotations (bold, italic, strikethrough, underline, code)
		if annotations, ok := rt["annotations"].(map[string]any); ok {
			if code, _ := annotations["code"].(bool); code {
				content = "`" + content + "`"
			}
			if bold, _ := annotations["bold"].(bool); bold {
				content = "**" + content + "**"
			}
			if italic, _ := annotations["italic"].(bool); italic {
				content = "*" + content + "*"
			}
			if strikethrough, _ := annotations["strikethrough"].(bool); strikethrough {
				content = "~~" + content + "~~"
			}
			// Note: underline has no standard markdown equivalent
		}

		// Apply link if present
		if linkURL != "" {
			content = "[" + content + "](" + linkURL + ")"
		}

		text.WriteString(content)
	}
	return text.String()
}

func extractTableMarkdown(b map[string]any) string {
	tableData, ok := b["table"].(map[string]any)
	if !ok {
		return ""
	}

	children, ok := tableData["children"].([]any)
	if !ok {
		return ""
	}

	var rows [][]string
	for _, child := range children {
		if row, ok := child.(map[string]any); ok {
			if rowData, ok := row["table_row"].(map[string]any); ok {
				if cells, ok := rowData["cells"].([]any); ok {
					var rowCells []string
					for _, cell := range cells {
						if cellItems, ok := cell.([]any); ok {
							// Use richTextToMarkdown to preserve formatting in table cells
							rowCells = append(rowCells, richTextToMarkdown(cellItems))
						}
					}
					rows = append(rows, rowCells)
				}
			}
		}
	}

	if len(rows) == 0 {
		return ""
	}

	var result strings.Builder
	for i, row := range rows {
		result.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 {
			sep := make([]string, len(row))
			for j := range sep {
				sep[j] = "---"
			}
			result.WriteString("| " + strings.Join(sep, " | ") + " |\n")
		}
	}
	return result.String()
}

func parseInlineMarkdown(text string) []map[string]any {
	var result []map[string]any
	i := 0

	for i < len(text) {
		// Bold: **text**
		if i+1 < len(text) && text[i] == '*' && text[i+1] == '*' {
			end := findClosing(text, i+2, "**")
			if end > 0 {
				result = append(result, map[string]any{
					"type":        "text",
					"text":        map[string]string{"content": text[i+2 : end]},
					"annotations": map[string]bool{"bold": true},
				})
				i = end + 2
				continue
			}
		}

		// Italic: *text*
		if text[i] == '*' && (i+1 >= len(text) || text[i+1] != '*') {
			end := findClosingSingle(text, i+1, '*')
			if end > 0 {
				result = append(result, map[string]any{
					"type":        "text",
					"text":        map[string]string{"content": text[i+1 : end]},
					"annotations": map[string]bool{"italic": true},
				})
				i = end + 1
				continue
			}
		}

		// Inline code: `text`
		if text[i] == '`' {
			end := findClosingSingle(text, i+1, '`')
			if end > 0 {
				result = append(result, map[string]any{
					"type":        "text",
					"text":        map[string]string{"content": text[i+1 : end]},
					"annotations": map[string]bool{"code": true},
				})
				i = end + 1
				continue
			}
		}

		// Link: [text](url) or page mention: [@Title](notion://page-id)
		if text[i] == '[' {
			closeBracket := findClosingSingle(text, i+1, ']')
			if closeBracket > 0 && closeBracket+1 < len(text) && text[closeBracket+1] == '(' {
				closeParen := findClosingSingle(text, closeBracket+2, ')')
				if closeParen > 0 {
					linkText := text[i+1 : closeBracket]
					linkURL := text[closeBracket+2 : closeParen]

					// Check for page mention: [@Title](notion://page-id)
					if strings.HasPrefix(linkURL, "notion://") && strings.HasPrefix(linkText, "@") {
						pageID := strings.TrimPrefix(linkURL, "notion://")
						result = append(result, map[string]any{
							"type": "mention",
							"mention": map[string]any{
								"type": "page",
								"page": map[string]any{
									"id": pageID,
								},
							},
						})
						i = closeParen + 1
						continue
					}

					// Regular link
					result = append(result, map[string]any{
						"type": "text",
						"text": map[string]any{
							"content": linkText,
							"link":    map[string]string{"url": linkURL},
						},
					})
					i = closeParen + 1
					continue
				}
			}
		}

		// Regular text - find next special char or end of string
		start := i
		for i < len(text) && text[i] != '*' && text[i] != '`' && text[i] != '[' {
			i++
		}
		if i > start {
			result = append(result, map[string]any{
				"type": "text",
				"text": map[string]string{"content": text[start:i]},
			})
		} else {
			// Special char without valid closing - treat as literal text and advance
			result = append(result, map[string]any{
				"type": "text",
				"text": map[string]string{"content": string(text[i])},
			})
			i++
		}
	}

	if len(result) == 0 {
		return []map[string]any{
			{"type": "text", "text": map[string]string{"content": text}},
		}
	}
	return result
}

func parseMarkdownTable(rows []string) map[string]any {
	if len(rows) < 2 {
		return nil
	}

	parseCells := func(row string) []string {
		row = strings.TrimSpace(row)
		if len(row) > 0 && row[0] == '|' {
			row = row[1:]
		}
		if len(row) > 0 && row[len(row)-1] == '|' {
			row = row[:len(row)-1]
		}
		parts := strings.Split(row, "|")
		var cells []string
		for _, p := range parts {
			cells = append(cells, strings.TrimSpace(p))
		}
		return cells
	}

	isSeparator := func(row string) bool {
		row = strings.TrimSpace(row)
		for _, c := range row {
			if c != '|' && c != '-' && c != ':' && c != ' ' {
				return false
			}
		}
		return strings.Contains(row, "-")
	}

	var dataRows [][]string
	for _, row := range rows {
		if !isSeparator(row) {
			cells := parseCells(row)
			if len(cells) > 0 {
				dataRows = append(dataRows, cells)
			}
		}
	}

	if len(dataRows) == 0 {
		return nil
	}

	tableWidth := len(dataRows[0])

	var tableRowBlocks []map[string]any
	for _, cells := range dataRows {
		for len(cells) < tableWidth {
			cells = append(cells, "")
		}
		var notionCells [][]map[string]any
		for _, cell := range cells[:tableWidth] {
			notionCells = append(notionCells, parseInlineMarkdown(cell))
		}
		tableRowBlocks = append(tableRowBlocks, map[string]any{
			"object": "block",
			"type":   "table_row",
			"table_row": map[string]any{
				"cells": notionCells,
			},
		})
	}

	return map[string]any{
		"object": "block",
		"type":   "table",
		"table": map[string]any{
			"table_width":       tableWidth,
			"has_column_header": true,
			"has_row_header":    false,
			"children":          tableRowBlocks,
		},
	}
}

func findClosing(text string, pos int, marker string) int {
	for i := pos; i <= len(text)-len(marker); i++ {
		if text[i:i+len(marker)] == marker {
			return i
		}
	}
	return -1
}

func findClosingSingle(text string, pos int, marker byte) int {
	for i := pos; i < len(text); i++ {
		if text[i] == marker {
			return i
		}
	}
	return -1
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// mapLanguageToNotion maps markdown language hints to Notion's expected language values.
// Notion has a specific list of supported languages.
func mapLanguageToNotion(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return "plain text"
	}

	// Map common aliases to Notion's expected values
	languageMap := map[string]string{
		"plaintext":  "plain text",
		"plain":      "plain text",
		"text":       "plain text",
		"txt":        "plain text",
		"js":         "javascript",
		"ts":         "typescript",
		"py":         "python",
		"rb":         "ruby",
		"sh":         "shell",
		"bash":       "shell",
		"zsh":        "shell",
		"yml":        "yaml",
		"dockerfile": "docker",
		"md":         "markdown",
	}

	if mapped, ok := languageMap[lang]; ok {
		return mapped
	}

	// Return as-is if it's a known Notion language
	knownLanguages := map[string]bool{
		"abap": true, "arduino": true, "assembly": true, "bash": true,
		"c": true, "c#": true, "c++": true, "clojure": true, "coffeescript": true,
		"css": true, "dart": true, "diff": true, "docker": true, "elixir": true,
		"elm": true, "erlang": true, "flow": true, "fortran": true, "f#": true,
		"gherkin": true, "glsl": true, "go": true, "graphql": true, "groovy": true,
		"haskell": true, "html": true, "java": true, "javascript": true, "json": true,
		"julia": true, "kotlin": true, "latex": true, "less": true, "lisp": true,
		"livescript": true, "lua": true, "makefile": true, "markdown": true,
		"markup": true, "matlab": true, "mermaid": true, "nix": true,
		"objective-c": true, "ocaml": true, "pascal": true, "perl": true,
		"php": true, "plain text": true, "powershell": true, "prolog": true,
		"protobuf": true, "python": true, "r": true, "reason": true, "ruby": true,
		"rust": true, "sass": true, "scala": true, "scheme": true, "scss": true,
		"shell": true, "sql": true, "swift": true, "typescript": true,
		"vb.net": true, "verilog": true, "vhdl": true, "visual basic": true,
		"webassembly": true, "xml": true, "yaml": true, "java/c/c++/c#": true,
	}

	if knownLanguages[lang] {
		return lang
	}

	// Default to plain text for unknown languages
	return "plain text"
}
