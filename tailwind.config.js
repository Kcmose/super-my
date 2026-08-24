/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        brand: {
          400: '#34d399',
          500: '#10b981',
          600: '#059669',
        },
        dark: {
          950: '#070a13',
          900: '#0c111e',
          850: '#111827',
          800: '#161f32',
          700: '#232f48',
          600: '#374766',
        },
      },
      animation: {
        'pulse-fast': 'pulse 1.5s cubic-bezier(0.4, 0, 0.6, 1) infinite',
      },
    },
  },
  plugins: [],
}
