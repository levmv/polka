import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const browserTestDir = dirname(fileURLToPath(import.meta.url));
const authStateDir = process.env.POLKA_AUTH_STATE_DIR || join(browserTestDir, '.auth');

export const authSetupPattern = /auth\.setup\.ts/;
export const laneAAdminStorageState = join(authStateDir, 'lane-a-admin.json');
export const laneBAdminStorageState = join(authStateDir, 'lane-b-admin.json');
export const pagerAdminStorageState = join(authStateDir, 'pager-admin.json');
