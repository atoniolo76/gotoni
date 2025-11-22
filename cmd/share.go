/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/atoniolo76/gotoni/pkg/client"
	"github.com/psanford/wormhole-william/wormhole"
	"github.com/spf13/cobra"
)

// shareCmd represents the share command
var shareCmd = &cobra.Command{
	Use:   "share <instance-id>",
	Short: "Securely share an instance's SSH key with another user",
	Long:  `Securely share an instance's SSH key using the Magic Wormhole protocol. Generates a code that the receiver can use to download the key.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		instanceID := args[0]

		// 1. Lookup SSH key for the instance
		sshKeyPath, err := client.GetSSHKeyForInstance(instanceID)
		if err != nil {
			log.Fatalf("Error finding SSH key for instance %s: %v", instanceID, err)
		}

		// Verify file exists
		if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
			log.Fatalf("SSH key file does not exist at path: %s", sshKeyPath)
		}

		fileName := filepath.Base(sshKeyPath)
		fmt.Printf("Preparing to share SSH key for instance '%s'\n", instanceID)
		fmt.Printf("Key file: %s\n\n", sshKeyPath)

		// 2. Initialize Wormhole client
		var c wormhole.Client

		ctx := context.Background()

		// 3. Generate code and offer file
		// We use SendText first to send metadata (instance ID + key name), then we could send the file.
		// However, for simplicity and robustness, let's just send the file content directly.
		// But wait, the user asked to "move the ssh key into their .ssh folder".
		// Sending the file via wormhole.SendFile is the standard way.

		f, err := os.Open(sshKeyPath)
		if err != nil {
			log.Fatalf("Failed to open key file: %v", err)
		}
		defer f.Close()

		// Get file size
		/*
			fi, err := f.Stat()
			if err != nil {
				log.Fatalf("Failed to stat key file: %v", err)
			}
		*/

		// We need to send the instance ID as well so the receiver knows what to name the host config.
		// A common pattern in wormhole is to send a directory or just a single file.
		// To keep it simple: we send the key file. The filename usually carries the key name.
		// The receiver will have to specify the instance name/ID or we can assume the filename IS the key name.
		// Let's stick to sending just the file for now.

		fmt.Println("Generating secure code...")

		// SendFile in newer wormhole-william versions accepts just the parameters.
		// The 4th argument is simply the file size as int64 in some versions or requires options.
		// Let's check the signature if possible or assume standard int.
		// Wait, the linter complained about int(fi.Size()) not being a SendOption.
		// The signature is SendFile(ctx, name, r, opts...).
		// There isn't a file size argument directly? It's likely an option.
		// Let's try to just pass the file content without size first if it accepts reader?
		// Actually, we should read the whole file into bytes and use SendText? No, SendFile is better.
		//
		// Checking common usage:
		// c.SendFile(ctx, name, content, int(size)) might be old.
		// c.SendFile(ctx, name, content, wormhole.WithFile(f))?
		//
		// If we look at recent docs/examples:
		// The method signature is: SendFile(ctx context.Context, name string, r io.Reader, opts ...SendOption) (string, <-chan SendResult, error)
		// We need a way to specify size if it's not inferred.
		// `wormhole.WithFile(f)` is often used if f is an os.File to get size automatically.

		// Let's try using just os.File which implements Stat() and see if the library uses it?
		// No, we have to pass it explicitly usually.
		// Let's search for existing usages or just fix it by not passing the size if not needed or using the right option?
		// The linter said `undefined: wormhole.WithFileLength`.
		// Maybe it is just `int64`? No, linter said `int` doesn't implement `SendOption`.
		// So it IS expecting options.
		// Let's try without size info? But receiver needs to know progress.
		//
		// Let's try to pass NO options.
		code, status, err := c.SendFile(ctx, fileName, f)
		if err != nil {
			log.Fatalf("Failed to start send: %v", err)
		}

		fmt.Printf("\nShare this code with the receiver:\n\n")
		fmt.Printf("\t%s\n\n", code)
		fmt.Println("Waiting for receiver to connect...")

		// Wait for transfer to complete
		select {
		case s := <-status:
			if s.Error != nil {
				log.Fatalf("Transfer failed: %v", s.Error)
			} else if s.OK {
				fmt.Println("\nTransfer completed successfully!")
			}
		case <-time.After(10 * time.Minute):
			log.Fatal("Transfer timed out after 10 minutes")
		}
	},
}

func init() {
	rootCmd.AddCommand(shareCmd)
}
