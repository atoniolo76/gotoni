### Docker Integration Test Instructions

Since the Docker daemon is not available in this environment, the integration test `tests/run_docker_ssh_test.sh` cannot be fully executed automatically here.

However, the script is fully prepared for you to run locally. It performs the following:

1.  **Generates a Test SSH Key**: Creates a temporary key pair.
2.  **Builds & Runs an SSH Container**: Uses a lightweight Ubuntu-based Docker container acting as a remote server.
3.  **Configures Authentication**: Installs the test public key into the container.
4.  **Runs a Go Integration Test**:
    -   Updates your local (mocked) `~/.ssh/config` using the `gotoni` library code.
    -   Adds the Docker container's port (mapped to localhost) to the config.
    -   Executes a real `ssh -F config_file <alias> echo success` command.
    -   This verifies that the config entry created by `gotoni` is valid and usable by system SSH tools (and thus by Cursor/VS Code).

To run it locally:

```bash
./tests/run_docker_ssh_test.sh
```

### Summary of Changes

1.  **SSH Config Update**:
    -   Implemented `UpdateSSHConfig` in `pkg/client/ssh.go` to safely append host entries to `~/.ssh/config`.
    -   Ensures `IdentityFile` points to the absolute path of the key.
    -   Sets `StrictHostKeyChecking no` to facilitate automated connections.

2.  **`gotoni open` Command**:
    -   Created `cmd/open.go`.
    -   Uses `cursor` command by default (checks PATH), falls back to `code` if missing.
    -   Constructs the URI `vscode-remote://ssh-remote+<instance-name><path>`.
    -   Handles both absolute and relative paths (e.g., `gotoni open my-vm src` opens `/home/ubuntu/src`).

3.  **`gotoni launch` Integration**:
    -   Updated `cmd/launch.go` to call `UpdateSSHConfig` automatically when `--wait` is used.
    -   Prints helpful instructions on how to connect using `ssh <name>` or `gotoni open <name>`.

You can now perform the full workflow:
1.  `gotoni launch my-dev-box --type gpu_1x_a100 --region us-east-1 --wait`
2.  (Wait for launch...)
3.  `gotoni open my-dev-box` -> Opens Cursor connected to the machine.

