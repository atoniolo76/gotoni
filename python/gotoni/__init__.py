import os
import subprocess
import time
import modal

def add_ssh(image: modal.Image, key_path: str = "~/.ssh/id_rsa.pub") -> modal.Image:
    """
    Modifies a Modal image to configure an SSH daemon, allowing secure remote access.
    
    This function performs the following setup steps on the provided modal.Image:
    1. Installs the 'openssh-server' package via apt.
    2. Generates host keys using 'ssh-keygen -A'.
    3. Modifies '/etc/ssh/sshd_config' to:
       - Allow root login ('PermitRootLogin yes').
       - Enable public key authentication ('PubkeyAuthentication yes').
       - Disable password authentication ('PasswordAuthentication no').
    4. Copies the specified local SSH public key to the container's '/root/.ssh/authorized_keys'.
    5. Sets strict permissions (chmod 600) on the 'authorized_keys' file to ensure SSH accepts it.
    
    Args:
        image (modal.Image): The base Modal image to modify.
        key_path (str): The path to your local public SSH key. 
                        Defaults to "~/.ssh/id_rsa.pub".
                        
    Returns:
        modal.Image: The updated Modal image with the SSH daemon and keys configured.
    """
    return (
        image
        .apt_install("openssh-server")
        .run_commands(
            "ssh-keygen -A",
            r"sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config",
            r"sed -i 's/^#\?PubkeyAuthentication.*/PubkeyAuthentication yes/' /etc/ssh/sshd_config",
            r"sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config",
        )
        .add_local_file(os.path.abspath(os.path.expanduser(key_path)), "/root/.ssh/authorized_keys", copy=True)
        .run_commands("chmod 600 /root/.ssh/authorized_keys")
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
    # Start the SSH daemon in the background
    subprocess.Popen(["/usr/sbin/sshd", "-D", "-e", "-p", str(port)])
    
    # Expose via Modal tunnel
    with modal.forward(port=port, unencrypted=True) as tunnel:
        host, tunnel_port = tunnel.tcp_socket
        print(f"\n=======================================================")
        print(f" SSH into container using command:")
        print(f"     ssh -p {tunnel_port} root@{host}")
        print(f"=======================================================\n")
        
        # Block to keep the container alive and tunnel active
        time.sleep(timeout)
