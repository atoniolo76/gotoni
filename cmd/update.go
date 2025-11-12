/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// updateCmd represents the update command
var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update gotoni to the latest version",
	Long:  `Download and install the latest version of gotoni from GitHub releases.`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := updateGotoni(); err != nil {
			log.Fatalf("Error updating gotoni: %v", err)
		}
		fmt.Println("✓ gotoni updated successfully!")
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

func updateGotoni() error {
	repo := "atoniolo76/gotoni"
	
	// Detect OS and architecture
	osName := runtime.GOOS
	arch := runtime.GOARCH
	
	// Normalize architecture names
	if arch == "amd64" {
		arch = "amd64"
	} else if arch == "arm64" {
		arch = "arm64"
	}
	
	// Determine binary name
	var binaryFile string
	if osName == "windows" {
		binaryFile = fmt.Sprintf("gotoni-%s-%s.exe", osName, arch)
	} else {
		binaryFile = fmt.Sprintf("gotoni-%s-%s", osName, arch)
	}
	
	// Get latest release URL
	latestURL := fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", repo, binaryFile)
	
	fmt.Printf("Updating gotoni...\n")
	fmt.Printf("Platform: %s-%s\n", osName, arch)
	fmt.Printf("Downloading from: %s\n", latestURL)
	
	// Create temporary file
	tmpFile, err := os.CreateTemp("", "gotoni-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	
	// Download binary
	resp, err := http.Get(latestURL)
	if err != nil {
		return fmt.Errorf("failed to download binary: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download binary: HTTP %d", resp.StatusCode)
	}
	
	// Write to temp file
	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return fmt.Errorf("failed to write binary: %w", err)
	}
	tmpFile.Close()
	
	// Make executable (Unix-like systems)
	if osName != "windows" {
		if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
			return fmt.Errorf("failed to make binary executable: %w", err)
		}
	}
	
	// Find current binary path
	currentBinary, err := exec.LookPath("gotoni")
	if err != nil {
		return fmt.Errorf("failed to find gotoni binary: %w\nMake sure gotoni is in your PATH", err)
	}
	
	// Resolve symlinks to get actual path
	actualPath, err := filepath.EvalSymlinks(currentBinary)
	if err != nil {
		actualPath = currentBinary
	}
	
	fmt.Printf("Current binary: %s\n", actualPath)
	
	// Check if we can write to the directory
	binDir := filepath.Dir(actualPath)
	if stat, err := os.Stat(binDir); err != nil {
		return fmt.Errorf("failed to stat binary directory: %w", err)
	} else if stat.Mode().Perm()&0200 == 0 {
		// Need sudo, use mv with sudo
		fmt.Println("Installing to", binDir, "(requires sudo)...")
		cmd := exec.Command("sudo", "mv", tmpFile.Name(), actualPath)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install binary with sudo: %w", err)
		}
	} else {
		// Can write directly
		// On Windows, we might need to close the current process, so try a different approach
		if osName == "windows" {
			// Try to remove the old binary first
			os.Remove(actualPath)
		}
		
		if err := os.Rename(tmpFile.Name(), actualPath); err != nil {
			// If rename fails (e.g., file in use on Windows), try copy + remove
			if err := copyFile(tmpFile.Name(), actualPath); err != nil {
				return fmt.Errorf("failed to install binary: %w", err)
			}
			os.Remove(tmpFile.Name())
		}
	}
	
	return nil
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	
	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()
	
	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}
	
	// Preserve permissions on Unix-like systems
	if runtime.GOOS != "windows" {
		sourceInfo, err := os.Stat(src)
		if err == nil {
			os.Chmod(dst, sourceInfo.Mode())
		}
	}
	
	return nil
}

