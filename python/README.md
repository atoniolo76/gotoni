# gotoni (Python Package)

A Python package providing utilities to easily inject an SSH daemon into [Modal](https://modal.com/) images and securely connect to running containers.

## Installation

```bash
pip install gotoni
```

## Usage

```python
import modal
from gotoni import add_ssh, start_ssh

app = modal.App("fused-moe")

image = modal.Image.debian_slim().uv_pip_install("gotoni", "cuda-tile")

image = add_ssh(image)

@app.function(image=image)
def main():
    start_ssh()
```
