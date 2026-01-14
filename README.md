# efficient-notion-mcp

An MCP (Model Context Protocol) server providing efficient bidirectional sync between Notion pages and local markdown files.

## Why This Exists

The official Notion MCP tools work well for reading and small edits, but updating entire pages is painfully slow. When you need to push changes back to Notion, the naive approach deletes blocks one-by-one, making N API calls for N blocks. On a page with 50 blocks, that's 50 sequential API calls just to clear the content before you can write new content.

This server uses **efficient bulk operations** that make page updates dramatically faster.

## Key Efficiency Techniques

### 1. Single-Call Content Erasure

Instead of deleting blocks one-by-one:
```
DELETE /blocks/{block1}  # 200ms
DELETE /blocks/{block2}  # 200ms
DELETE /blocks/{block3}  # 200ms
... (N times)
```

We use the `erase_content` parameter:
```
PATCH /pages/{page_id}
{ "erase_content": true }  # Single call, ~300ms total
```

This undocumented (but stable) Notion API feature clears all page content in one request.

### 2. Batched Block Appends

Blocks are appended in batches of 100 (the API maximum) rather than one-by-one:
```
PATCH /blocks/{page_id}/children
{ "children": [block1, block2, ..., block100] }
```

### 3. User Name Caching

Comment author names are resolved once and cached, avoiding repeated `/users/{id}` calls when the same user has multiple comments.

### 4. Markdown Round-Trip

Pages are converted to markdown for local editing, preserving:
- Headings, paragraphs, lists (bullet, numbered, checkbox)
- Tables (with proper Notion table block conversion)
- Code blocks with language hints
- Links, bold, italic, inline code
- Comments (as blockquotes with author attribution)
- Page metadata (in YAML frontmatter)

## Installation

```bash
go install github.com/vthunder/efficient-notion-mcp@latest
```

Or build from source:
```bash
git clone https://github.com/vthunder/efficient-notion-mcp
cd efficient-notion-mcp
go build -o efficient-notion-mcp .
```

## Configuration

Set your Notion API key:
```bash
export NOTION_API_KEY=secret_xxx
```

Or create a `.env` file:
```
NOTION_API_KEY=secret_xxx
```

### Claude Desktop / Claude Code

Add to your MCP configuration:

```json
{
  "mcpServers": {
    "notion-sync": {
      "command": "/path/to/efficient-notion-mcp",
      "env": {
        "NOTION_API_KEY": "secret_xxx"
      }
    }
  }
}
```

## Tools

### `notion_pull`

Download a Notion page as a markdown file.

**Parameters:**
- `page_id` (required): Notion page ID (with or without dashes)
- `output_dir` (optional): Directory for output file (default: `/tmp/notion`)

**Output:** Creates `{Title}.md` with YAML frontmatter containing the page ID.

**Example:**
```
notion_pull("1dd479aa-ad74-8065-bf23-d90ae1ca3560")
→ /tmp/notion/My-Page.md
```

### `notion_push`

Push a local markdown file back to Notion. **Fast**: uses erase+replace, not block-by-block deletion.

**Parameters:**
- `file_path` (required): Path to markdown file (must have `notion_id` in frontmatter)

**Example:**
```
notion_push("/tmp/notion/My-Page.md")
→ Page updated in Notion
```

### `notion_diff`

Compare local markdown against live Notion content.

**Parameters:**
- `file_path` (required): Path to markdown file (must have `notion_id` in frontmatter)

**Example:**
```
notion_diff("/tmp/notion/My-Page.md")
→ Shows added/removed lines
```

## Markdown Format

Pulled pages include YAML frontmatter:

```markdown
---
notion_id: 1dd479aaad748065bf23d90ae1ca3560
title: My Page Title
pulled_at: 2024-01-15T10:30:00Z
---

# My Page Title

Content here...

---

## Comments

> **Dan Mills** *(Jan 14, 2024)*: Great work on this!
```

The `notion_id` is required for push/diff operations. Comments are preserved as blockquotes during round-trips.

## Performance Comparison

For a page with 50 blocks:

| Operation | Naive Approach | This Server |
|-----------|----------------|-------------|
| Clear page | ~10s (50 DELETEs) | ~0.3s (1 PATCH) |
| Write content | ~10s (50 PATCHes) | ~0.5s (1 PATCH) |
| **Total** | **~20s** | **~0.8s** |

**25x faster** for typical page updates.

## Notion Integration Setup

1. Go to [Notion Integrations](https://www.notion.so/my-integrations)
2. Create a new integration
3. Copy the "Internal Integration Secret"
4. Share your pages/databases with the integration

## Supported Block Types

**Reading (Notion → Markdown):**
- Headings (1-3)
- Paragraphs
- Bullet lists
- Numbered lists
- To-do lists (checkboxes)
- Quotes/callouts
- Code blocks
- Tables
- Dividers
- Comments (as blockquotes)

**Writing (Markdown → Notion):**
- All of the above
- Inline formatting: **bold**, *italic*, `code`, [links](url)

## Limitations

- Nested lists are flattened (Notion API limitation for appending)
- Images and files are not synced (only text content)
- Database pages: properties are not synced, only page content
- Comments: existing Notion comments are preserved as blockquotes, but new blockquotes don't become Notion comments

## License

MPL-2.0 - See [LICENSE](LICENSE)

## Credits

Developed for use with [Claude Code](https://docs.anthropic.com/en/docs/claude-code) and the Model Context Protocol.
