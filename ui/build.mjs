// Bundles the interface into one self-contained HTML file that the Go
// binary embeds: ../internal/ui/dist/index.html
import { build } from 'esbuild';
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';

const out = await build({
  entryPoints: ['src/main.jsx'],
  bundle: true,
  minify: true,
  write: false,
  format: 'iife',
  target: 'es2020',
  jsx: 'automatic',
  jsxImportSource: 'preact',
  legalComments: 'none',
});
const js = out.outputFiles[0].text.replace(/<\/script/g, '<\\/script');
const css = readFileSync('src/styles.css', 'utf8');
const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bandwidth Pilot</title>
<style>${css}</style>
</head>
<body>
<div id="app"></div>
<script>/*__PILOT_BOOT__*/</script>
<script>${js}</script>
</body>
</html>
`;
mkdirSync('../internal/ui/dist', { recursive: true });
writeFileSync('../internal/ui/dist/index.html', html);
console.log(`built index.html (${(html.length / 1024).toFixed(1)} KB)`);
