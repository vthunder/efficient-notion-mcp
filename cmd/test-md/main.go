package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/vthunder/efficient-notion-mcp/notion"
)

func main() {
	input, _ := io.ReadAll(os.Stdin)
	blocks := notion.MarkdownToBlocks(string(input))

	// Pretty print the blocks
	out, _ := json.MarshalIndent(blocks, "", "  ")
	fmt.Println(string(out))

	// Convert back to markdown
	fmt.Println("\n--- Round-trip markdown ---")
	fmt.Println(notion.BlocksToMarkdown(blocks))
}
