# Sample GPU Instance Launcher

This script demonstrates how to launch the lowest-tier GPU instance on Lambda Labs using the GPU Snapshot provider interface.

## Prerequisites

1. **Lambda Labs Account**: Sign up at [cloud.lambda.ai](https://cloud.lambda.ai)
2. **API Token**: Get your API token from Lambda Labs dashboard
3. **SSH Key**: Optional - the script will automatically create one if you don't have any configured

## Setup

1. **Set your API token**:
   ```bash
   export LAMBDA_API_TOKEN="your_api_token_here"
   ```

2. **SSH Key Setup** (optional):
   - The script automatically creates SSH keys if none exist
   - Keys are stored securely in `~/.gpusnapshot/ssh-keys/`
   - You can also pre-configure keys in Lambda Labs dashboard

## Binary Deployment

When compiled to a binary, the script:

- **Works from any directory** (no working directory dependencies)
- **Creates secure SSH key storage** in user home directory
- **Handles cross-platform paths** automatically
- **Generates unique key names** to avoid conflicts on multiple runs

### Environment Variables

```bash
export LAMBDA_API_TOKEN="your_token"  # Required
# Optional: override key storage location
export GPU_SNAPSHOT_SSH_DIR="$HOME/my-custom-ssh-dir"
```

### Directory Structure Created

```
~/.gpusnapshot/
└── ssh-keys/
    ├── gpu-instance-sample-instance-key-20241201-123456.pem
    └── gpu-instance-sample-instance-key-20241201-123457.pem  # Multiple runs
```

## Usage

```bash
# From the project root
cd cmd/sample_instance
go run main.go
```

## Analyzer Tool

The `cmd/analyzer` tool analyzes YAML config files for GPU compatibility:

```bash
# Generate example config
go run cmd/analyzer/main.go

# Analyze existing config
go run cmd/analyzer/main.go --config your-config.yaml --provider lambdalabs

# With API token for instance recommendations
export LAMBDA_API_TOKEN="your_token"
go run cmd/analyzer/main.go --config your-config.yaml
```

## What it does

1. **Finds the lowest-cost GPU instance** with available capacity
2. **Launches the instance** in the first available region
3. **Waits for the instance to be ready**
4. **Prints access information** including:
   - Public IP address
   - Jupyter Lab URL and token (if available)
   - SSH access command

## Example Output

```
🚀 Launching lowest-tier GPU instance on lambdalabs...
🔐 Checking SSH keys...
   No SSH keys found. Creating a new one...
   ✅ Created SSH key: sample-instance-key
   🔑 Private key saved to: /Users/username/.gpusnapshot/ssh-keys/gpu-instance-sample-instance-key-20241201-123456.pem
   🛡️  Keep this file secure - it's your only way to access the instance!
   💡 The key is stored in your home directory under ~/.gpusnapshot/ssh-keys/
📊 Selected: gpu_1x_rtx6000 ($0.50/hour)
   - Description: 1x RTX 6000 (24 GB)
   - GPUs: 1 x RTX 6000 (24 GB)
   - CPU: 14 vCPUs
   - RAM: 46 GB
   - Storage: 512 GB
   - Available regions: [us-south-1]
🌍 Using region: us-south-1
⚡ Launching instance...
✅ Instance launched! ID: abc123def456
⏳ Waiting for instance to be ready...
   Status: booting
   Status: active
🎉 Instance is ready!
📍 Public IP: 198.51.100.2
🧠 Jupyter Lab: https://jupyter-xyz.lambdaspaces.com/?token=abc123
🔑 Jupyter Token: abc123
💡 SSH Command: ssh -i /Users/username/.gpusnapshot/ssh-keys/gpu-instance-sample-instance-key-20241201-123456.pem ubuntu@198.51.100.2

⚠️  REMEMBER TO TERMINATE THE INSTANCE WHEN DONE ⚠️
💰 Cost: $0.50/hour
```

## Important Notes

- **Costs money**: GPU instances cost real money while running
- **Terminate when done**: Always terminate instances you don't need
- **SSH Key storage**: Keys are saved to `~/.gpusnapshot/ssh-keys/` with unique timestamps
- **Binary compatibility**: Works when compiled to binary - uses proper user directories
- **Multiple runs**: Each run creates a new unique key file (no conflicts)
- **Cross-platform**: Handles Windows/macOS/Linux path differences automatically
- **Automatic SSH setup**: The script handles SSH key creation automatically
- **Region selection**: Currently uses first available region (could be improved)

## Next Steps

This is a basic example. In production, you'd want to add:
- Proper error handling
- Instance termination logic
- Region selection
- SSH key management
- Timeout handling
- Logging
- Configuration files
