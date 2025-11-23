# gotoni

![gotoni running a remote command](./docs/Terminal.gif)

Automate Lambda.ai with Ansible-inspired Go CLI

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/atoniolo76/gotoni/main/install.sh | bash
```

## Setup

Export your [Lambda API key](https://cloud.lambda.ai/api-keys/cloud-api):

```bash
export LAMBDA_API_KEY=your_token_here
```

## Workflow

### 1. Launch an Instance (with Filesystem)

Launch an instance with a persistent filesystem attached. If the filesystem doesn't exist, it will be created. The `--wait` flag ensures the command polls until the instance is ready.

```bash
gotoni launch my-project \
  -t gpu_1x_a10 \
  --filesystem my-data \
  --wait
```

### 2. Open in IDE

Once launched, instantly open the remote workspace in your preferred IDE (defaults to Cursor).

```bash
gotoni open my-project
```

Or force VS Code:

```bash
gotoni open my-project --code
```

### 3. Share Access Securely

Share SSH access with a friend or colleague using Magic Wormhole.

**Sender:**
```bash
gotoni share my-project
# Generates a secure code like: 7-guitarist-revenge
```

**Receiver:**
```bash
gotoni receive 7-guitarist-revenge
# Automatically configures SSH keys and host entry
ssh my-project
```

## Commands Reference

- `gotoni launch` - Launch instances (supports waiting and filesystems)
- `gotoni open` - Open remote instance in Cursor/VS Code
- `gotoni share` - Securely share SSH access
- `gotoni receive` - Receive SSH access and auto-configure
- `gotoni list` - List active instances
- `gotoni available` - List available instance types and regions
- `gotoni run` - Execute remote commands
- `gotoni delete` - Terminate instances
- `gotoni filesystems` - Manage filesystems
- `gotoni ssh-keys` - Manage SSH keys

## Configuration

Configuration is automatically managed in a local SQLite database.

