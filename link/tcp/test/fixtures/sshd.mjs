import { once } from 'node:events'
import { createServer } from 'node:net'
import { readFile, writeFile } from 'node:fs/promises'
import { userInfo } from 'node:os'
import { join } from 'node:path'
import { exec, processFor } from './hosts.mjs'

export async function availablePort() {
  const reservation = createServer()
  reservation.listen(0, '127.0.0.1')
  await once(reservation, 'listening')
  const port = reservation.address().port
  await new Promise(resolve => reservation.close(resolve))
  return port
}

export async function sshServer(env, forwarding) {
  const port = await availablePort()
  const hostKey = join(env.dir, 'ssh-host')
  const identity = join(env.dir, 'ssh-client')
  const sshdConfig = join(env.dir, 'sshd_config')
  const knownHosts = join(env.dir, 'known_hosts')
  for (const key of [hostKey, identity]) {
    await exec('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', key])
  }
  const otherKey = join(env.dir, 'ssh-ecdsa')
  await exec('ssh-keygen', ['-q', '-t', 'ecdsa', '-N', '', '-f', otherKey])
  await writeFile(knownHosts, `[127.0.0.1]:${port} ` + await readFile(hostKey + '.pub', 'utf8'))
  await writeFile(sshdConfig, [
    'ListenAddress 127.0.0.1', `Port ${port}`, `HostKey "${hostKey}"`, `HostKey "${otherKey}"`,
    `PidFile "${env.dir}/sshd.pid"`, `AuthorizedKeysFile "${identity}.pub"`,
    `AllowUsers ${userInfo().username}`, 'AuthenticationMethods publickey', 'PasswordAuthentication no',
    'KbdInteractiveAuthentication no', ...forwarding,
    'GatewayPorts no', 'MaxSessions 0',
  ].join('\n') + '\n')
  const daemon = processFor(env, process.env.SSHD ?? '/usr/sbin/sshd', ['-D', '-e', '-f', sshdConfig])
  await daemon.waitFor(/Server listening on 127\.0\.0\.1/)
  return { port, identity, hostKey, knownHosts }
}
