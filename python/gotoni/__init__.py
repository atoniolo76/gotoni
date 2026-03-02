import os
import subprocess
import time
import modal

_KEY_CANDIDATES = [
    "~/.ssh/id_ed25519.pub",
    "~/.ssh/id_rsa.pub",
    "~/.ssh/id_ecdsa.pub",
    "~/.ssh/id_dsa.pub",
]

def _resolve_key_path(key_path: str | None) -> str:
    if key_path is not None:
        resolved = os.path.abspath(os.path.expanduser(key_path))
        if not os.path.exists(resolved):
            raise FileNotFoundError(f"SSH public key not found: {resolved}")
        return resolved
    for candidate in _KEY_CANDIDATES:
        resolved = os.path.abspath(os.path.expanduser(candidate))
        if os.path.exists(resolved):
            return resolved
    raise FileNotFoundError(
        "No SSH public key found in ~/.ssh/. "
        "Generate one with: ssh-keygen -t ed25519"
    )


def add_ssh(image: modal.Image, key_path: str | None = None) -> modal.Image:
    """
    Modifies a Modal image to configure an SSH daemon, allowing secure remote access.
    
    This function performs the following setup steps on the provided modal.Image:
    1. Installs the 'openssh-server' package via apt.
    2. Generates host keys using 'ssh-keygen -A'.
    3. Modifies '/etc/ssh/sshd_config' to:
       - Allow root login ('PermitRootLogin yes').
       - Enable public key authentication ('PubkeyAuthentication yes').
       - Disable password authentication ('PasswordAuthentication no').
       - Disable StrictModes to avoid home-directory permission issues in containers.
       - Disable UsePAM since minimal images (e.g. debian_slim) lack a full PAM setup.
    4. Copies the local SSH public key to the container's '/root/.ssh/authorized_keys'.
       Auto-detects the key type (ed25519, rsa, ecdsa, dsa) when key_path is not given.
    5. Sets strict permissions (chmod 600) on the 'authorized_keys' file.
    
    Args:
        image (modal.Image): The base Modal image to modify.
        key_path (str | None): Path to your local public SSH key.
                               When None (default), auto-detects the first available key
                               in ~/.ssh/ (tries ed25519, rsa, ecdsa, dsa in order).
                        
    Returns:
        modal.Image: The updated Modal image with the SSH daemon and keys configured.
    """
    # When Modal imports this module inside a running container it re-executes
    # module-level code (including add_ssh calls) to locate the decorated
    # function.  The image is already built at that point, so skip everything.
    if os.environ.get("MODAL_TASK_ID"):
        return image

    resolved_key_path = _resolve_key_path(key_path)
    pubkey = open(resolved_key_path).read().strip()

    return image.run_commands(
        "apt-get update && apt-get install -y openssh-server && rm -rf /var/lib/apt/lists/*",
        "mkdir -p /root/.ssh && chmod 700 /root/.ssh",
        f"echo '{pubkey}' > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys",
        "ssh-keygen -A",
        r"sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config",
        r"sed -i 's/^#\?PubkeyAuthentication.*/PubkeyAuthentication yes/' /etc/ssh/sshd_config",
        r"sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config",
        r"sed -i 's/^#\?StrictModes.*/StrictModes no/' /etc/ssh/sshd_config || echo 'StrictModes no' >> /etc/ssh/sshd_config",
        r"sed -i 's/^#\?UsePAM.*/UsePAM no/' /etc/ssh/sshd_config || echo 'UsePAM no' >> /etc/ssh/sshd_config",
    )


def start_ssh(port: int = 2222, timeout: int = 3600):
    """
    Starts the SSH daemon in the Modal container and sets up a port forward to the user's local machine.
    
    This function should be called inside your Modal function when you want to block and keep 
    the container alive for an SSH session. It performs these actions:
    1. Spawns the SSH daemon subprocess: `/usr/sbin/sshd -D -e -p <port>`.
    2. Uses `modal.forward` to expose the chosen port over an unencrypted Modal tunnel.
    3. Prints out the exact `ssh` command the user can copy and paste into their local 
       terminal or IDE to connect to the container.
    4. Blocks execution (sleeps) for the specified `timeout` duration to keep the container running.
    
    Args:
        port (int): The port on which the SSH daemon will listen inside the container. 
                    Defaults to 2222.
        timeout (int): The number of seconds to keep the container running and the tunnel open. 
                       Defaults to 3600 (1 hour).
    """
    os.makedirs("/run/sshd", exist_ok=True)
    subprocess.run(["/usr/sbin/sshd", "-t"], check=True)

    proc = subprocess.Popen(
        ["/usr/sbin/sshd", "-D", "-e", "-p", str(port)],
        stderr=subprocess.PIPE,
    )

    # Give sshd a moment to bind and confirm it hasn't crashed on startup
    time.sleep(1)
    if proc.poll() is not None:
        stderr_output = proc.stderr.read().decode() if proc.stderr else ""
        raise RuntimeError(f"sshd exited immediately (code {proc.returncode}):\n{stderr_output}")

    with modal.forward(port=port, unencrypted=True) as tunnel:
        host, tunnel_port = tunnel.tcp_socket
        print(f"\n=======================================================")
        print(f" SSH into container using command:")
        print(f"     ssh -p {tunnel_port} root@{host}")
        print(f"=======================================================\n")

        time.sleep(timeout)
