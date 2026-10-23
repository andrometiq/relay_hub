export default {
  content: ['./index.html', './src/**/*.{vue,js}'],
  theme: {
    extend: {
      colors: {
        surface: {
          white: 'var(--surface-white)', modal: 'var(--surface-modal)', selected: 'var(--surface-selected)',
          gray: { 1: 'var(--surface-gray-1)', 2: 'var(--surface-gray-2)', 3: 'var(--surface-gray-3)', 4: 'var(--surface-gray-4)', 5: 'var(--surface-gray-5)', 6: 'var(--surface-gray-6)', 7: 'var(--surface-gray-7)' },
          blue: { 1: 'var(--surface-blue-1)' }, amber: { 1: 'var(--surface-amber-1)' }, red: { 1: 'var(--surface-red-1)' }, green: { 1: 'var(--surface-green-1)' },
        },
        ink: {
          white: 'var(--ink-white)',
          gray: { 1: 'var(--ink-gray-1)', 2: 'var(--ink-gray-2)', 3: 'var(--ink-gray-3)', 4: 'var(--ink-gray-4)', 5: 'var(--ink-gray-5)', 6: 'var(--ink-gray-6)', 7: 'var(--ink-gray-7)', 8: 'var(--ink-gray-8)', 9: 'var(--ink-gray-9)' },
          blue: { 1: 'var(--ink-blue-1)', 2: 'var(--ink-blue-2)', 3: 'var(--ink-blue-3)' }, red: { 1: 'var(--ink-red-1)', 2: 'var(--ink-red-2)', 3: 'var(--ink-red-3)' }, green: { 1: 'var(--ink-green-1)', 2: 'var(--ink-green-2)' },
        },
        outline: {
          gray: { DEFAULT: 'var(--outline-gray-2)', 1: 'var(--outline-gray-1)', 2: 'var(--outline-gray-2)', 3: 'var(--outline-gray-3)', 4: 'var(--outline-gray-4)', 5: 'var(--outline-gray-5)', modals: 'var(--outline-gray-2)' },
          blue: { 1: 'var(--outline-blue-1)', 2: 'var(--outline-blue-2)' }, red: { 1: 'var(--outline-red-1)' },
        },
      },
      fontFamily: { sans: ['Inter', 'ui-sans-serif', 'system-ui', 'sans-serif'] },
      fontSize: { base: ['13px', { lineHeight: '1.5' }] },
    },
  },
}
