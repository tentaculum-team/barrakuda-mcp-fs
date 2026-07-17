package main

import (
	"fmt"
	"os"

	"barrakuda-mcp-fs/internal/mcp"
	"barrakuda-mcp-fs/internal/repository"
	"barrakuda-mcp-fs/internal/service"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

func main() {
	repo := repository.NewFileRepository()
	fileService, err := service.NewFileService(repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to establish sandbox root:", err)
		os.Exit(1)
	}

	// BARRAKUDA_FS_GRANTS troca o local do grant file (testes / setups custom).
	grantsPath := os.Getenv("BARRAKUDA_FS_GRANTS")
	if grantsPath == "" {
		var err error
		grantsPath, err = service.DefaultGrantsPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "failed to locate grants file:", err)
			os.Exit(1)
		}
	}
	grants, err := service.LoadGrantStore(grantsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to load grants file", grantsPath+":", err)
		os.Exit(1)
	}
	fileService.SetGrantStore(grants)

	mcpServer := mcp.NewServer(fileService, grants)

	if err := mcpserver.ServeStdio(mcpServer); err != nil {
		fmt.Fprintln(os.Stderr, "server error:", err)
		os.Exit(1)
	}
}
