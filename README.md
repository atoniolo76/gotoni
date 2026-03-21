# gotoni

![gotoni running a remote command](./docs/Terminal.gif)

gotoni lets you manage, share, and interact with your GPUs/Sandboxes over SSH.

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/atoniolo76/gotoni/main/install.sh | bash
```

## Setup

Export your cloud's API key:

```bash
# Lambda.ai
export LAMBDA_API_KEY=xxx
# Modal
export MODAL_TOKEN_ID=your_token_id
export MODAL_TOKEN_SECRET=your_token_secret
# Orgo
export ORGO_API_KEY=your_token_here
```

### SpiceDB (Access Control)

Optional. When configured, gotoni gates CRUD operations through SpiceDB
permission checks. Without it, gotoni works as before with no access control.

```bash
export SPICEDB_ENDPOINT=http://localhost:8444
export SPICEDB_TOKEN=your_preshared_key
export GOTONI_ORG_ID=acme
export GOTONI_PROJECT_ID=ml-team   # optional, scopes to a project within the org
```

```bash
gotoni admin org create acme            # creates org, you become admin
gotoni admin org invite acme editor     # prints a wormhole code to share
gotoni admin join <wormhole-code>       # join an org from another machine
```

## Workflow

### 1. Launch an Instance

Launch an instance and `--wait` until the instance is ready.

```bash
gotoni launch my-project \
  -t gpu_1x_a10 \
  --filesystem my-data \
  --wait
```

### 2. Open in IDE

Once launched, open the remote workspace in your preferred IDE. You must specify
`--cursor` or `--code`.

> **Prerequisite:** Ensure you have installed the command-line tool for your
> IDE.
>
> - **Cursor:** `Cmd+Shift+P` > "Install 'cursor' command"
> - **VS Code:** `Cmd+Shift+P` > "Install 'code' command"

```bash
gotoni open my-project --cursor
gotoni open my-project --code
gotoni open my-project /home/ubuntu/repo --cursor   # open a specific path
```

### 3. Share Access Securely

Grants SSH access to your project partner or research buddy using the Magic
Wormhole protocol.

Works with both Lambda (SSH key + IP) and Modal (SSH key + tunnel) instances.

**Sender (Lambda):**

```bash
gotoni share              # auto-picks first running instance
gotoni share my-project   # specific instance
```

**Sender (Modal):**

```bash
export GOTONI_CLOUD=modal
gotoni share              # auto-picks first running sandbox
gotoni share sb-abc123    # specific sandbox ID
```

**Receiver:**

```bash
gotoni receive <wormhole-code>
```

## Add Tasks on creation

Create tasks that will be executed during instance launch. You can specify
services to run in the background like an inference server.

### Example: Install vLLM and Serve Kimi-K2-Thinking

```bash
gotoni tasks add --name "install vllm" --command "sudo apt install -y python3-pip && pip install vllm"
gotoni tasks add --name "run kimi" --command "vllm serve moonshotai/Kimi-K2-Thinking" --depends-on "install dependencies" --type service
```

**Launch with Tasks:**

```bash
gotoni launch ml-instance
  -t gpu_1x_a100
  -r us-west-1
  --wait
  --tasks "install vllm,run kimi"
```

## Commands Reference

### Instance Management

- `gotoni launch` - Launch instances (supports waiting, filesystems, and tasks)
- `gotoni list` - List running instances
- `gotoni status` - Check status of services running on an instance
- `gotoni delete` - Terminate instances
- `gotoni available` - List available instance types and regions
- `gotoni price` - Show pricing for instance types

### Connect & Share

- `gotoni open` - Open remote instance in Cursor (`--cursor`) or VS Code
  (`--code`)
- `gotoni connect` - Connect to a remote instance via SSH or IDE
- `gotoni share` - Securely share instance access (Lambda + Modal)
- `gotoni receive` - Receive shared access and auto-configure SSH
- `gotoni run` - Execute a command on a remote instance

### Automation

- `gotoni tasks` - Manage automation tasks for instance provisioning
- `gotoni provision` - Run automation tasks on an existing instance
- `gotoni serve` - Serve models on vLLM across instances and regions

### Infrastructure

- `gotoni filesystems` - Manage persistent filesystems
- `gotoni ssh-keys` - Manage SSH keys
- `gotoni firewall` - Manage firewall rules
- `gotoni sync` - Sync SSH keys from running instances to local database
- `gotoni gpu` - Track GPU memory usage over time

### Cluster & Load Balancing

- `gotoni cluster` - Manage cluster nodes (status, setup, uploads)
- `gotoni sglang` - Manage SGLang servers on cluster instances
- `gotoni proxy` - Manage centralized proxy for SGLang server pools
- `gotoni lb` - Manage local load balancer processes
- `gotoni lb-deploy` - Build, upload, and start load balancer on instances
- `gotoni tokenizer` - Manage tokenizer sidecar on cluster instances

### Utilities

- `gotoni update` - Update gotoni to the latest version
- `gotoni check` - Parse and analyze a Dockerfile
