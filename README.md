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

> **Prerequisite:** Ensure you have installed the command-line tool for your IDE.
> - **Cursor:** `Cmd+Shift+P` > "Install 'cursor' command"
> - **VS Code:** `Cmd+Shift+P` > "Install 'code' command"

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

## Add Tasks on creation

Create tasks that can be executed on instances during launch. You can also start services that will run in the background with systemd, such as an inference server.

### Example: Install and Build vLLM

```bash
gotoni tasks add  --name "install depedencies" --command "pip install vllm"

gotoni tasks add --name "run vllm" --command "vllm serve moonshotai/Kimi-K2-Thinking" --depends-on "install dependencies" --type service
```

### Launch with Tasks

```bash
gotoni launch ml-instance
  -t gpu_1x_a100
  -r us-west-1
  --wait
  --tasks "Install Build Tools,Clone llama.cpp,Build llama.cpp"
```


## Commands Reference

- `gotoni launch` - Launch instances (supports waiting, filesystems, and tasks)
- `gotoni open` - Open remote instance in Cursor/VS Code
- `gotoni share` - Securely share SSH access
- `gotoni receive` - Receive SSH access and auto-configure
- `gotoni tasks` - Manage automation tasks
- `gotoni provision` - Run tasks on an existing instance
- `gotoni list` - List active instances
- `gotoni available` - List available instance types and regions
- `gotoni run` - Execute remote commands
- `gotoni delete` - Terminate instances
- `gotoni filesystems` - Manage filesystems
- `gotoni ssh-keys` - Manage SSH keys

## Configuration

Configuration is automatically managed in a local SQLite database.

