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

## Automation Tasks

Create reusable automation tasks that can be executed on instances during launch or provisioning. Tasks support commands, services, dependencies, and more.

### Creating Tasks

**Example: Install and Build vLLM**

```bash
# Install dependencies
gotoni tasks add \
  --name "Install vLLM Dependencies" \
  --command "pip install torch transformers accelerate"

# Install vLLM
gotoni tasks add \
  --name "Install vLLM" \
  --command "pip install vllm" \
  --depends-on "Install vLLM Dependencies"
```

**Example: Download and Build llama.cpp**

```bash
# Install build tools
gotoni tasks add \
  --name "Install Build Tools" \
  --command "sudo apt-get update && sudo apt-get install -y build-essential cmake"

# Clone llama.cpp
gotoni tasks add \
  --name "Clone llama.cpp" \
  --command "git clone https://github.com/ggerganov/llama.cpp.git" \
  --working-dir "/home/ubuntu" \
  --depends-on "Install Build Tools"

# Build llama.cpp with CUDA support
gotoni tasks add \
  --name "Build llama.cpp" \
  --command "make LLAMA_CUDA=1 -j$(nproc)" \
  --working-dir "/home/ubuntu/llama.cpp" \
  --depends-on "Clone llama.cpp"
```

**Example: Start a Service**

```bash
# Run vLLM server as a background service
gotoni tasks add \
  --name "Start vLLM Server" \
  --type service \
  --command "python -m vllm.entrypoints.openai.api_server --model meta-llama/Llama-2-7b-hf --port 8000" \
  --working-dir "/home/ubuntu" \
  --depends-on "Install vLLM" \
  --restart always \
  --restart-sec 10
```

### Using Tasks

**Launch with Tasks:**

```bash
gotoni launch ml-instance \
  -t gpu_1x_a100 \
  -r us-west-1 \
  --wait \
  --tasks "Install Build Tools,Clone llama.cpp,Build llama.cpp"
```

**Provision Existing Instance:**

```bash
# Runs all defined tasks on the instance
gotoni provision my-instance
```

**List Tasks:**

```bash
gotoni tasks list
```

**Delete Tasks:**

```bash
gotoni tasks delete "Install Build Tools"
```

### Task Features

- **Dependencies:** Tasks can depend on other tasks with `--depends-on`
- **Working Directory:** Set working directory with `--working-dir`
- **Environment Variables:** Pass environment variables with `--env KEY=VALUE`
- **Services:** Run background services with `--type service`
- **Restart Policies:** Configure systemd restart behavior for services
- **Conditional Execution:** Use `--when` for conditional task execution

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

