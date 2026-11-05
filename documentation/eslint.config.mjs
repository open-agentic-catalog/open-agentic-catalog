import js from '@eslint/js';
import globals from 'globals';

// Check authored Node tooling; rendered pages, caches and packages have other owners.
export default [
  { ignores: ['**/node_modules/**', '.astro/**', 'dist/**', 'public/generated/**', 'src/content/docs/generated/**'] },
  {
    files: ['**/*.mjs'],
    rules: js.configs.recommended.rules,
    languageOptions: { globals: globals.nodeBuiltin },
  },
];
