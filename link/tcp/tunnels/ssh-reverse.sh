#!/bin/sh
# ssh_config supplies the relay address, user, identity and trusted host key.
set -eu
if [ "$#" -ne 4 ]; then
  echo 'usage: ssh-reverse.sh SSH_CONFIG HOST_ALIAS FORWARD_PORT LISTEN_PORT' >&2
  exit 2
fi
for port in "$3" "$4"; do
  case "$port" in ''|*[!0-9]*) echo 'expected numeric TCP ports' >&2; exit 2 ;; esac
done
exec ssh -F "$1" -N -T -o BatchMode=yes -o StrictHostKeyChecking=yes \
  -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
  -R "127.0.0.1:$3:127.0.0.1:$4" -- "$2"
