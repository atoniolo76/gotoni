# Dockerfile for SSH testing
FROM ubuntu:latest

# Install OpenSSH Server
RUN apt-get update && apt-get install -y openssh-server iproute2

# Configure SSH
RUN mkdir /var/run/sshd
RUN echo 'root:password' | chpasswd
RUN sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin yes/' /etc/ssh/sshd_config

# Create a test user 'ubuntu' with password 'password' for simple auth testing if keys fail,
# but we'll set up keys in the test script.
RUN useradd -m -s /bin/bash ubuntu && echo 'ubuntu:password' | chpasswd

# Create .ssh directory for ubuntu user
RUN mkdir -p /home/ubuntu/.ssh && chmod 700 /home/ubuntu/.ssh && chown ubuntu:ubuntu /home/ubuntu/.ssh

# Expose SSH port
EXPOSE 22

CMD ["/usr/sbin/sshd", "-D"]

