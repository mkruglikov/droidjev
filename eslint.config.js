// Minimal lint net for a zero-dependency repo: `npm run lint` runs the pinned
// eslint via npx, so nothing is ever installed. Core rules only — the point is what
// `node --check` cannot see (unused vars, ==, stray var), not style policing.
export default [
  {
    files: ['src/**/*.js', 'test/**/*.mjs'],
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      globals: {
        console: 'readonly',
        process: 'readonly',
        performance: 'readonly',
        fetch: 'readonly',
        AbortController: 'readonly',
        AbortSignal: 'readonly',
        Buffer: 'readonly',
        TextDecoder: 'readonly',
        Uint8Array: 'readonly',
        setTimeout: 'readonly',
        clearTimeout: 'readonly',
      },
    },
    rules: {
      'no-unused-vars': ['error', { argsIgnorePattern: '^_', caughtErrors: 'none' }],
      'no-var': 'error',
      'prefer-const': 'error',
      eqeqeq: ['error', 'always'],
    },
  },
];
