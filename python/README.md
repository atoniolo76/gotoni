# gotoni (Python Package)

A Python package providing utilities to easily inject an SSH daemon into [Modal](https://modal.com/) images and securely connect to running containers.

## Installation

```bash
pip install gotoni
```

## Usage

```python
import modal
import gotoni

# 1. Add SSH into your Modal Image
image = modal.Image.debian_slim().pip_install("fastapi")
image = gotoni.add_ssh(image, key_path="~/.ssh/id_rsa.pub")

app = modal.App("my-ssh-app", image=image)

# 2. Start the SSH daemon in a modal function
@app.function(timeout=3600)
def debug_session():
    # This will print the SSH command to your terminal and block for the duration of the timeout
    gotoni.start_ssh(port=2222, timeout=3600)
```
