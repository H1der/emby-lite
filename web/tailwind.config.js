/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        cinema: {
          bg: '#0b0e14',
          surface: '#141923',
          card: '#1a2230',
          border: '#263042',
          accent: '#10b981',
        },
      },
    },
  },
  plugins: [],
}
