# gotoni
Turn your Neocloud into an inference engine with <100ms cold-starts.

## Usage

Launch an instance (creates SSH key automatically):
```bash
gotoni launch --instance-type gpu_1x_a100_sxm4 --region us-east-1
```

Connect to a running instance:
```bash
gotoni connect <instance-ip>
```

List running instances:
```bash
gotoni list --running
```

Terminate instances:
```bash
gotoni delete <instance-id>
```

## How It Works

1. **Launch**: Creates a new SSH key pair, saves the key to `~/.gotoni/config.yaml`, and launches the instance
2. **Connect**: Reads the SSH key from config and connects to the specified instance IP
3. Each launch creates a fresh SSH key for security

## Setup

Set your Lambda API token:
```bash
export LAMBDA_API_KEY=your_token_here
```

The config file `~/.gotoni/config.yaml` is automatically managed - you don't need to create it manually.

