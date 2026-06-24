import { copyFile, mkdir, rm } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const source = join(root, 'src', 'static');
const dist = join(root, 'dist');
const files = ['index.html', 'app.js', 'styles.css', 'phlox-logo.svg'];

await rm(dist, { recursive: true, force: true });
await mkdir(dist, { recursive: true });

for (const file of files) {
  await copyFile(join(source, file), join(dist, file));
}

console.log(`Built frontend assets into ${dist}`);
