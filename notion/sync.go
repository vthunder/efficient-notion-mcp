// Package notion provides efficient Notion page sync utilities.
//
// Key efficiency techniques:
//   - Uses PATCH /pages/{id} with erase_content=true for single-call content clearing
//   - Batches block appends (100 blocks per request)
//   - Caches user name lookups to reduce API calls
//   - Converts bidirectionally between Notion blocks and Markdown
package notion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Debug controls whether debug logging is enabled.
// Set NOTION_DEBUG=1 to enable.
var Debug = os.Getenv("NOTION_DEBUG") == "1"

func debugLog(format string, args ...any) {
	if Debug {
		log.Printf("[notion] "+format, args...)
	}
}

const (
	notionAPIBase    = "https://api.notion.com/v1"
	notionAPIVersion = "2022-06-28"
)

// Client handles Notion API operations with efficiency optimizations.
type Client struct {
	apiKey     string
	httpClient *http.Client
	userCache  map[string]string // user ID -> name cache
}

// NewClient creates a new client using NOTION_API_KEY env var.
func NewClient() (*Client, error) {
	apiKey := os.Getenv("NOTION_API_KEY")
	if apiKey == "" {
		apiKey = loadFromEnvFile("NOTION_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("NOTION_API_KEY not found in environment or .env file")
	}
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		userCache: make(map[string]string),
	}, nil
}

// loadFromEnvFile reads a key from .env file in current directory.
func loadFromEnvFile(key string) string {
	data, err := os.ReadFile(".env")
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			val := strings.TrimPrefix(line, prefix)
			return strings.Trim(val, `"'`)
		}
	}
	return ""
}

// Comment represents a Notion comment.
type Comment struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_time"`
	BlockID   string    `json:"block_id,omitempty"`
}

// PullResult contains the result of pulling a page.
type PullResult struct {
	Markdown   string
	FilePath   string
	PageID     string
	Title      string
	ChildPages []string // IDs of child pages for restoration
}

// PullPage fetches a Notion page with comments and saves as markdown.
func (c *Client) PullPage(pageID string, outputDir string) (*PullResult, error) {
	pageID = strings.ReplaceAll(pageID, "-", "")

	title, err := c.getPageTitle(pageID)
	if err != nil {
		title = pageID
	}

	blocks, err := c.fetchAllBlocks(pageID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch blocks: %w", err)
	}

	// Find child pages and determine which are trailing (after last non-child_page content)
	var childPageIDs []string
	trailingChildPages := make(map[string]bool)

	// First pass: collect all child page IDs
	for _, block := range blocks {
		blockType, _ := block["type"].(string)
		if blockType == "child_page" {
			blockID, _ := block["id"].(string)
			if blockID != "" {
				childPageIDs = append(childPageIDs, blockID)
			}
		}
	}

	// Second pass: find which child pages are trailing
	// Trailing = any child_page after the last non-child_page block
	lastNonChildPageIdx := -1
	for i, block := range blocks {
		blockType, _ := block["type"].(string)
		if blockType != "child_page" {
			lastNonChildPageIdx = i
		}
	}

	for i, block := range blocks {
		blockType, _ := block["type"].(string)
		if blockType == "child_page" && i > lastNonChildPageIdx {
			blockID, _ := block["id"].(string)
			if blockID != "" {
				trailingChildPages[blockID] = true
			}
		}
	}

	debugLog("PullPage: found %d child pages, %d trailing", len(childPageIDs), len(trailingChildPages))

	comments, _ := c.fetchComments(pageID)

	// Convert blocks to markdown, with child pages as mentions (except trailing ones)
	markdown := BlocksToMarkdownWithChildPages(blocks, trailingChildPages)

	if len(comments) > 0 {
		markdown += "\n---\n\n## Comments\n\n"
		for _, comment := range comments {
			date := comment.CreatedAt.Format("Jan 2, 2006")
			markdown += fmt.Sprintf("> **%s** *(%s)*: %s\n\n", comment.Author, date, comment.Content)
		}
	}

	if outputDir == "" {
		outputDir = "/tmp/notion"
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output dir: %w", err)
	}

	safeTitle := sanitizeFilename(title)
	filePath := filepath.Join(outputDir, safeTitle+".md")

	// Build frontmatter with child_pages if any
	frontmatter := fmt.Sprintf("---\nnotion_id: %s\ntitle: %s\npulled_at: %s\n",
		pageID, title, time.Now().Format(time.RFC3339))
	if len(childPageIDs) > 0 {
		frontmatter += "child_pages:\n"
		for _, cpID := range childPageIDs {
			frontmatter += fmt.Sprintf("  - %s\n", cpID)
		}
	}
	frontmatter += "---\n\n"
	content := frontmatter + markdown

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return &PullResult{
		Markdown:   content,
		FilePath:   filePath,
		PageID:     pageID,
		Title:      title,
		ChildPages: childPageIDs,
	}, nil
}

// PushPage reads a markdown file and pushes to Notion.
// Child pages tracked in frontmatter are re-parented after the push
// so they appear at the bottom of the page.
func (c *Client) PushPage(filePath string) error {
	debugLog("PushPage: reading %s", filePath)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	pageID, childPageIDs, markdown := parseFrontmatterFull(string(content))
	if pageID == "" {
		return fmt.Errorf("no notion_id found in frontmatter")
	}
	debugLog("PushPage: page_id=%s, content_len=%d, child_pages=%d", pageID, len(markdown), len(childPageIDs))

	markdown, preservedComments := extractCommentsSection(markdown)

	// Convert markdown to blocks
	blocks := MarkdownToBlocks(markdown)
	debugLog("PushPage: converted to %d blocks", len(blocks))

	if preservedComments != "" {
		blocks = append(blocks, map[string]any{
			"object":  "block",
			"type":    "divider",
			"divider": map[string]any{},
		})
		blocks = append(blocks, map[string]any{
			"object": "block",
			"type":   "heading_2",
			"heading_2": map[string]any{
				"rich_text": []map[string]any{
					{"type": "text", "text": map[string]string{"content": "Comments"}},
				},
			},
		})
		blocks = append(blocks, MarkdownToBlocks(preservedComments)...)
	}

	// Simple approach: erase + replace + reparent
	debugLog("PushPage: erasing page content")
	if err := c.erasePage(pageID); err != nil {
		return fmt.Errorf("failed to erase page: %w", err)
	}

	debugLog("PushPage: appending %d blocks", len(blocks))
	if err := c.appendBlocksBatched(pageID, blocks); err != nil {
		return fmt.Errorf("failed to append blocks: %w", err)
	}

	// Re-parent child pages to restore them at the bottom
	if len(childPageIDs) > 0 {
		debugLog("PushPage: re-parenting %d child pages", len(childPageIDs))
		if err := c.reparentPages(pageID, childPageIDs); err != nil {
			return fmt.Errorf("failed to reparent child pages: %w", err)
		}
	}

	debugLog("PushPage: complete")
	return nil
}

// DiffPage compares local markdown against current Notion content.
func (c *Client) DiffPage(filePath string) (string, error) {
	localContent, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	pageID, localMarkdown := parseFrontmatter(string(localContent))
	if pageID == "" {
		return "", fmt.Errorf("no notion_id found in frontmatter")
	}

	blocks, err := c.fetchAllBlocks(pageID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch blocks: %w", err)
	}

	notionMarkdown := BlocksToMarkdown(blocks)

	localLines := strings.Split(strings.TrimSpace(localMarkdown), "\n")
	notionLines := strings.Split(strings.TrimSpace(notionMarkdown), "\n")

	var diff strings.Builder
	diff.WriteString(fmt.Sprintf("Comparing %s against Notion page %s\n\n", filePath, pageID))

	maxLines := len(localLines)
	if len(notionLines) > maxLines {
		maxLines = len(notionLines)
	}

	hasChanges := false
	for i := 0; i < maxLines; i++ {
		var local, notion string
		if i < len(localLines) {
			local = localLines[i]
		}
		if i < len(notionLines) {
			notion = notionLines[i]
		}

		if local != notion {
			hasChanges = true
			if notion != "" {
				diff.WriteString(fmt.Sprintf("- %s\n", notion))
			}
			if local != "" {
				diff.WriteString(fmt.Sprintf("+ %s\n", local))
			}
		}
	}

	if !hasChanges {
		return "No changes detected.", nil
	}

	return diff.String(), nil
}

// fetchAllBlocks recursively fetches all blocks including comments.
func (c *Client) fetchAllBlocks(blockID string) ([]map[string]any, error) {
	var allBlocks []map[string]any
	cursor := ""

	for {
		url := fmt.Sprintf("%s/blocks/%s/children?page_size=100", notionAPIBase, blockID)
		if cursor != "" {
			url += "&start_cursor=" + cursor
		}

		resp, err := c.doRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}

		var result struct {
			Results    []map[string]any `json:"results"`
			HasMore    bool             `json:"has_more"`
			NextCursor string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		for i, block := range result.Results {
			blockID, _ := block["id"].(string)
			if blockID == "" {
				continue
			}

			hasChildren, _ := block["has_children"].(bool)
			if hasChildren {
				children, err := c.fetchBlockChildren(blockID)
				if err == nil && len(children) > 0 {
					blockType, _ := block["type"].(string)
					if blockData, ok := block[blockType].(map[string]any); ok {
						blockData["children"] = children
						result.Results[i][blockType] = blockData
					}
				}
			}
		}

		allBlocks = append(allBlocks, result.Results...)

		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
	}

	return allBlocks, nil
}

// fetchBlockChildren fetches immediate children of a block.
func (c *Client) fetchBlockChildren(blockID string) ([]any, error) {
	var allChildren []any
	cursor := ""

	for {
		url := fmt.Sprintf("%s/blocks/%s/children?page_size=100", notionAPIBase, blockID)
		if cursor != "" {
			url += "&start_cursor=" + cursor
		}

		resp, err := c.doRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}

		var result struct {
			Results    []map[string]any `json:"results"`
			HasMore    bool             `json:"has_more"`
			NextCursor string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, err
		}

		for _, r := range result.Results {
			allChildren = append(allChildren, r)
		}

		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
	}

	return allChildren, nil
}

// fetchComments fetches all comments for a page or block.
func (c *Client) fetchComments(blockID string) ([]Comment, error) {
	url := fmt.Sprintf("%s/comments?block_id=%s&page_size=100", notionAPIBase, blockID)

	resp, err := c.doRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []struct {
			ID          string `json:"id"`
			CreatedTime string `json:"created_time"`
			CreatedBy   struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"created_by"`
			RichText []struct {
				PlainText string `json:"plain_text"`
			} `json:"rich_text"`
			Parent struct {
				BlockID string `json:"block_id"`
			} `json:"parent"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse comments: %w", err)
	}

	var comments []Comment
	for _, r := range result.Results {
		var content strings.Builder
		for _, rt := range r.RichText {
			content.WriteString(rt.PlainText)
		}

		authorName := r.CreatedBy.Name
		if authorName == "" && r.CreatedBy.ID != "" {
			authorName = c.resolveUserName(r.CreatedBy.ID)
		}

		createdAt, _ := time.Parse(time.RFC3339, r.CreatedTime)
		comments = append(comments, Comment{
			ID:        r.ID,
			Author:    authorName,
			Content:   content.String(),
			CreatedAt: createdAt,
			BlockID:   r.Parent.BlockID,
		})
	}

	return comments, nil
}

// resolveUserName fetches and caches user name by ID.
func (c *Client) resolveUserName(userID string) string {
	if name, ok := c.userCache[userID]; ok {
		return name
	}

	url := fmt.Sprintf("%s/users/%s", notionAPIBase, userID)
	resp, err := c.doRequest("GET", url, nil)
	if err != nil {
		c.userCache[userID] = "Unknown"
		return "Unknown"
	}

	var user struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp, &user); err != nil {
		c.userCache[userID] = "Unknown"
		return "Unknown"
	}

	name := user.Name
	if name == "" {
		name = "Unknown"
	}
	c.userCache[userID] = name
	return name
}

// getPageTitle fetches the title of a page.
func (c *Client) getPageTitle(pageID string) (string, error) {
	url := fmt.Sprintf("%s/pages/%s", notionAPIBase, pageID)

	resp, err := c.doRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	var result struct {
		Properties map[string]struct {
			Title []struct {
				PlainText string `json:"plain_text"`
			} `json:"title"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", err
	}

	for _, prop := range result.Properties {
		if len(prop.Title) > 0 {
			return prop.Title[0].PlainText, nil
		}
	}

	return "", fmt.Errorf("no title found")
}

// erasePage clears all content using PATCH with erase_content=true.
// This is MUCH faster than deleting blocks one by one.
func (c *Client) erasePage(pageID string) error {
	url := fmt.Sprintf("%s/pages/%s", notionAPIBase, pageID)
	body := map[string]any{
		"erase_content": true,
	}
	_, err := c.doRequest("PATCH", url, body)
	return err
}

// getChildPageIDs returns the page IDs of all child_page blocks in a page.
// This is a bulk operation (one API call per 100 blocks), not block-by-block.
func (c *Client) getChildPageIDs(pageID string) ([]string, error) {
	var childPageIDs []string
	cursor := ""

	for {
		url := fmt.Sprintf("%s/blocks/%s/children?page_size=100", notionAPIBase, pageID)
		if cursor != "" {
			url += "&start_cursor=" + cursor
		}

		resp, err := c.doRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}

		var result struct {
			Results    []map[string]any `json:"results"`
			HasMore    bool             `json:"has_more"`
			NextCursor string           `json:"next_cursor"`
		}
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		for _, block := range result.Results {
			blockType, _ := block["type"].(string)
			if blockType == "child_page" {
				if childPage, ok := block["child_page"].(map[string]any); ok {
					if title, ok := childPage["title"].(string); ok {
						// The block ID is the child page ID for child_page blocks
						if blockID, ok := block["id"].(string); ok {
							debugLog("getChildPageIDs: found child page %q with ID %s", title, blockID)
							childPageIDs = append(childPageIDs, blockID)
						}
					}
				}
			}
		}

		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
	}

	return childPageIDs, nil
}

// restorePages restores pages from trash by setting archived=false.
func (c *Client) restorePages(pageIDs []string) error {
	for _, pageID := range pageIDs {
		debugLog("restorePages: restoring page %s from trash", pageID)
		url := fmt.Sprintf("%s/pages/%s", notionAPIBase, pageID)
		body := map[string]any{
			"archived": false,
		}
		if _, err := c.doRequest("PATCH", url, body); err != nil {
			return fmt.Errorf("failed to restore page %s: %w", pageID, err)
		}
	}
	return nil
}

// reparentPages moves child pages back under the parent page.
// This is called after erase+replace to restore the parent-child relationship.
func (c *Client) reparentPages(parentPageID string, childPageIDs []string) error {
	for _, childID := range childPageIDs {
		debugLog("reparentPages: moving page %s under parent %s", childID, parentPageID)
		url := fmt.Sprintf("%s/pages/%s", notionAPIBase, childID)
		body := map[string]any{
			"parent": map[string]any{
				"page_id": parentPageID,
			},
			"archived": false, // Ensure it's not archived
		}
		if _, err := c.doRequest("PATCH", url, body); err != nil {
			return fmt.Errorf("failed to reparent page %s: %w", childID, err)
		}
	}
	return nil
}

// appendBlocksBatched appends blocks in batches of 100.
func (c *Client) appendBlocksBatched(pageID string, blocks []map[string]any) error {
	const batchSize = 100
	totalBatches := (len(blocks) + batchSize - 1) / batchSize

	for i := 0; i < len(blocks); i += batchSize {
		end := i + batchSize
		if end > len(blocks) {
			end = len(blocks)
		}
		batchNum := i/batchSize + 1

		batch := blocks[i:end]
		body := map[string]any{
			"children": batch,
		}

		debugLog("appendBlocksBatched: sending batch %d/%d (%d blocks)", batchNum, totalBatches, len(batch))
		url := fmt.Sprintf("%s/blocks/%s/children", notionAPIBase, pageID)
		if _, err := c.doRequest("PATCH", url, body); err != nil {
			return fmt.Errorf("failed to append batch %d: %w", i/batchSize, err)
		}
		debugLog("appendBlocksBatched: batch %d complete", batchNum)

		if end < len(blocks) {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return nil
}

// doRequest makes an authenticated request to Notion API.
func (c *Client) doRequest(method, url string, body any) ([]byte, error) {
	var bodyReader io.Reader
	var bodyLen int
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal body: %w", err)
		}
		bodyLen = len(data)
		bodyReader = bytes.NewReader(data)
	}

	debugLog("doRequest: %s %s (body: %d bytes)", method, url, bodyLen)
	start := time.Now()

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Notion-Version", notionAPIVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		debugLog("doRequest: failed after %v: %v", time.Since(start), err)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	debugLog("doRequest: %d (%d bytes) in %v", resp.StatusCode, len(respBody), time.Since(start))

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// Helper functions

func sanitizeFilename(name string) string {
	unsafe := regexp.MustCompile(`[<>:"/\\|?*]`)
	name = unsafe.ReplaceAllString(name, "_")
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

func parseFrontmatter(content string) (pageID, markdown string) {
	pageID, _, markdown = parseFrontmatterFull(content)
	return pageID, markdown
}

// parseFrontmatterFull parses frontmatter and returns page ID, child page IDs, and markdown content.
func parseFrontmatterFull(content string) (pageID string, childPages []string, markdown string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", nil, content
	}

	endIdx := strings.Index(content[4:], "\n---\n")
	if endIdx == -1 {
		return "", nil, content
	}

	frontmatter := content[4 : 4+endIdx]
	markdown = content[4+endIdx+5:]

	inChildPages := false
	for _, line := range strings.Split(frontmatter, "\n") {
		if strings.HasPrefix(line, "notion_id:") {
			pageID = strings.TrimSpace(strings.TrimPrefix(line, "notion_id:"))
			inChildPages = false
		} else if strings.HasPrefix(line, "child_pages:") {
			inChildPages = true
		} else if inChildPages && strings.HasPrefix(line, "  - ") {
			cpID := strings.TrimSpace(strings.TrimPrefix(line, "  - "))
			if cpID != "" {
				childPages = append(childPages, cpID)
			}
		} else if !strings.HasPrefix(line, "  ") {
			inChildPages = false
		}
	}

	return pageID, childPages, markdown
}

func extractCommentsSection(markdown string) (content, comments string) {
	idx := strings.Index(markdown, "\n## Comments\n")
	if idx == -1 {
		return markdown, ""
	}

	dividerIdx := strings.LastIndex(markdown[:idx], "\n---\n")
	if dividerIdx != -1 {
		return strings.TrimSpace(markdown[:dividerIdx]), strings.TrimSpace(markdown[idx+13:])
	}

	return strings.TrimSpace(markdown[:idx]), strings.TrimSpace(markdown[idx+13:])
}

// SchemaProperty describes a database property.
type SchemaProperty struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// QueryResult contains flattened database query results.
type QueryResult struct {
	Results    []map[string]any `json:"results"`
	HasMore    bool             `json:"has_more"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// GetSchema returns the schema of a database (property names and types).
func (c *Client) GetSchema(databaseID string) ([]SchemaProperty, error) {
	databaseID = strings.ReplaceAll(databaseID, "-", "")
	url := fmt.Sprintf("%s/databases/%s", notionAPIBase, databaseID)

	resp, err := c.doRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse schema: %w", err)
	}

	var schema []SchemaProperty
	for name, prop := range result.Properties {
		schema = append(schema, SchemaProperty{
			Name: name,
			Type: prop.Type,
		})
	}

	return schema, nil
}

// QueryDatabase queries a database and returns flattened results.
func (c *Client) QueryDatabase(databaseID string, filter map[string]any, sorts []map[string]any, limit int) (*QueryResult, error) {
	databaseID = strings.ReplaceAll(databaseID, "-", "")

	if limit <= 0 || limit > 100 {
		limit = 100
	}

	body := map[string]any{
		"page_size": limit,
	}
	if filter != nil {
		body["filter"] = filter
	}
	if sorts != nil {
		body["sorts"] = sorts
	}

	url := fmt.Sprintf("%s/databases/%s/query", notionAPIBase, databaseID)
	resp, err := c.doRequest("POST", url, body)
	if err != nil {
		return nil, err
	}

	var raw struct {
		Results []struct {
			ID         string                    `json:"id"`
			Properties map[string]map[string]any `json:"properties"`
		} `json:"results"`
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse query results: %w", err)
	}

	var results []map[string]any
	for _, page := range raw.Results {
		flat := map[string]any{
			"_id": page.ID,
		}
		for name, prop := range page.Properties {
			flat[name] = c.flattenProperty(prop)
		}
		results = append(results, flat)
	}

	return &QueryResult{
		Results:    results,
		HasMore:    raw.HasMore,
		NextCursor: raw.NextCursor,
	}, nil
}

// flattenProperty extracts the value from a Notion property object.
func (c *Client) flattenProperty(prop map[string]any) any {
	propType, _ := prop["type"].(string)

	switch propType {
	case "title":
		return extractRichText(prop["title"])
	case "rich_text":
		return extractRichText(prop["rich_text"])
	case "number":
		return prop["number"]
	case "select":
		if sel, ok := prop["select"].(map[string]any); ok {
			return sel["name"]
		}
		return nil
	case "multi_select":
		if arr, ok := prop["multi_select"].([]any); ok {
			var names []string
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					if name, ok := m["name"].(string); ok {
						names = append(names, name)
					}
				}
			}
			return names
		}
		return nil
	case "status":
		if status, ok := prop["status"].(map[string]any); ok {
			return status["name"]
		}
		return nil
	case "date":
		if date, ok := prop["date"].(map[string]any); ok {
			start, _ := date["start"].(string)
			end, _ := date["end"].(string)
			if end != "" {
				return map[string]string{"start": start, "end": end}
			}
			return start
		}
		return nil
	case "people":
		if arr, ok := prop["people"].([]any); ok {
			var names []string
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					if name, ok := m["name"].(string); ok {
						names = append(names, name)
					} else if id, ok := m["id"].(string); ok {
						names = append(names, c.resolveUserName(id))
					}
				}
			}
			return names
		}
		return nil
	case "checkbox":
		return prop["checkbox"]
	case "url":
		return prop["url"]
	case "email":
		return prop["email"]
	case "phone_number":
		return prop["phone_number"]
	case "created_time":
		return prop["created_time"]
	case "created_by":
		if user, ok := prop["created_by"].(map[string]any); ok {
			if name, ok := user["name"].(string); ok {
				return name
			}
			if id, ok := user["id"].(string); ok {
				return c.resolveUserName(id)
			}
		}
		return nil
	case "last_edited_time":
		return prop["last_edited_time"]
	case "last_edited_by":
		if user, ok := prop["last_edited_by"].(map[string]any); ok {
			if name, ok := user["name"].(string); ok {
				return name
			}
			if id, ok := user["id"].(string); ok {
				return c.resolveUserName(id)
			}
		}
		return nil
	case "formula":
		if formula, ok := prop["formula"].(map[string]any); ok {
			ftype, _ := formula["type"].(string)
			return formula[ftype]
		}
		return nil
	case "relation":
		if arr, ok := prop["relation"].([]any); ok {
			var ids []string
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					if id, ok := m["id"].(string); ok {
						ids = append(ids, id)
					}
				}
			}
			return ids
		}
		return nil
	case "rollup":
		if rollup, ok := prop["rollup"].(map[string]any); ok {
			rtype, _ := rollup["type"].(string)
			return rollup[rtype]
		}
		return nil
	case "files":
		if arr, ok := prop["files"].([]any); ok {
			var urls []string
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					if ftype, ok := m["type"].(string); ok {
						if fileData, ok := m[ftype].(map[string]any); ok {
							if url, ok := fileData["url"].(string); ok {
								urls = append(urls, url)
							}
						}
					}
				}
			}
			return urls
		}
		return nil
	default:
		return nil
	}
}

// extractRichText extracts plain text from rich_text array.
func extractRichText(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	var text strings.Builder
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			if pt, ok := m["plain_text"].(string); ok {
				text.WriteString(pt)
			}
		}
	}
	return text.String()
}
