/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // Semantic theme-aware colors backed by CSS variables
        brand: {
          DEFAULT: 'var(--accent)',
          hover: 'var(--accent-hover)',
          soft: 'var(--accent-soft)',
        },
        cinema: {
          bg: 'var(--surface-inset)',
          surface: 'var(--glass-bg)',
          card: 'var(--glass-card)',
          border: 'var(--glass-border)',
          accent: 'var(--accent)',
        },
      },
      screens: {
        '3xl': '1800px',
        '4xl': '2300px',
      },
      borderRadius: {
        '2xl': '1rem',
        '3xl': '1.375rem',
      },
    },
  },
  plugins: [],
}
