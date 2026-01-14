// efficient-notion-mcp provides MCP tools for efficient Notion page sync.
//
// Key features:
//   - Pull: Download Notion pages as markdown with frontmatter and comments
//   - Push: Upload markdown back to Notion (erase+replace, not block-by-block)
//   - Diff: Compare local markdown against live Notion content
//
// The push operation uses PATCH /pages/{id} with erase_content=true for
// single-call content clearing, which is dramatically faster than deleting
// blocks one by one.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/vthunder/efficient-notion-mcp/notion"
)

func main() {
	s := server.NewMCPServer(
		"efficient-notion-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Register tools
	s.AddTool(pullTool(), handlePull)
	s.AddTool(pushTool(), handlePush)
	s.AddTool(diffTool(), handleDiff)

	// Run server
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func pullTool() mcp.Tool {
	return mcp.NewTool("notion_pull",
		mcp.WithDescription("Pull a Notion page to a local markdown file. Fetches all blocks and comments, converts to markdown with frontmatter."),
		mcp.WithString("page_id",
			mcp.Required(),
			mcp.Description("Notion page ID (with or without dashes)"),
		),
		mcp.WithString("output_dir",
			mcp.Description("Directory to save the markdown file. Default: /tmp/notion"),
		),
	)
}

func handlePull(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, _ := req.Params.Arguments.(map[string]any)
	pageID, _ := args["page_id"].(string)
	outputDir, _ := args["output_dir"].(string)

	if pageID == "" {
		return mcp.NewToolResultError("page_id is required"), nil
	}

	client, err := notion.NewClient()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create client: %v", err)), nil
	}

	result, err := client.PullPage(pageID, outputDir)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to pull page: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Pulled page '%s' to %s\n\nPage ID: %s\nContent length: %d characters",
		result.Title, result.FilePath, result.PageID, len(result.Markdown),
	)), nil
}

func pushTool() mcp.Tool {
	return mcp.NewTool("notion_push",
		mcp.WithDescription("Push a local markdown file to Notion. Uses efficient erase+replace (not block-by-block deletion). File must have notion_id in frontmatter."),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("Path to the local markdown file (must have notion_id in frontmatter)"),
		),
	)
}

func handlePush(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, _ := req.Params.Arguments.(map[string]any)
	filePath, _ := args["file_path"].(string)

	if filePath == "" {
		return mcp.NewToolResultError("file_path is required"), nil
	}

	client, err := notion.NewClient()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create client: %v", err)), nil
	}

	if err := client.PushPage(filePath); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to push page: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully pushed %s to Notion", filePath)), nil
}

func diffTool() mcp.Tool {
	return mcp.NewTool("notion_diff",
		mcp.WithDescription("Compare a local markdown file against its Notion page. Shows what would change if pushed."),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("Path to the local markdown file (must have notion_id in frontmatter)"),
		),
	)
}

func handleDiff(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, _ := req.Params.Arguments.(map[string]any)
	filePath, _ := args["file_path"].(string)

	if filePath == "" {
		return mcp.NewToolResultError("file_path is required"), nil
	}

	client, err := notion.NewClient()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create client: %v", err)), nil
	}

	diff, err := client.DiffPage(filePath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to diff page: %v", err)), nil
	}

	return mcp.NewToolResultText(diff), nil
}
