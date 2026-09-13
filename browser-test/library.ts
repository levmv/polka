import { spawn } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const browserTestDir = dirname(fileURLToPath(import.meta.url));
export const binary = join(browserTestDir, '..', 'polka');

export async function serveLibrary<T>(
  directory: string,
  use: (baseURL: string, logs: () => string) => Promise<T>,
): Promise<T> {
  const server = spawn(
    binary,
    [
      'serve',
      '--addr',
      '127.0.0.1:0',
      '--data',
      directory,
      '--admin-user',
      'admin',
      '--admin-password',
      'devpass',
    ],
    { stdio: ['ignore', 'ignore', 'pipe'] },
  );
  let output = '';
  const stopped = new Promise<void>((resolve) => server.once('close', () => resolve()));
  let startupTimer: ReturnType<typeof setTimeout>;
  const ready = new Promise<string>((resolve, reject) => {
    startupTimer = setTimeout(() => reject(new Error('Browser-test server did not start')), 10_000);
    server.once('error', reject);
    server.once('exit', (code, signal) => {
      reject(new Error(`Browser-test server exited: ${signal || code}`));
    });
    server.stderr.on('data', (chunk: Buffer) => {
      output = (output + chunk.toString()).slice(-64 * 1024);
      const match = output.match(/Server listening on (http:\/\/127\.0\.0\.1:\d+)/);
      if (match) resolve(match[1]);
    });
  });
  try {
    const baseURL = await ready;
    clearTimeout(startupTimer!);
    return await use(baseURL, () => output);
  } catch (error) {
    if (output) process.stderr.write(output);
    throw error;
  } finally {
    clearTimeout(startupTimer!);
    server.kill('SIGTERM');
    const killTimer = setTimeout(() => server.kill('SIGKILL'), 5_000);
    try {
      await stopped;
    } finally {
      clearTimeout(killTimer);
    }
  }
}
