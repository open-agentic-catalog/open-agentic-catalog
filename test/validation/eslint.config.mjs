import js from '@eslint/js';
import globals from 'globals';

// Go tools and installed validator artifacts are outside the maintained MJS scope.
export default [
  { ignores: ['**/node_modules/**', '.tools/**', 'tools/**'] },
  {
    files: ['**/*.mjs'],
    rules: js.configs.recommended.rules,
    languageOptions: { globals: globals.nodeBuiltin },
  },
];
