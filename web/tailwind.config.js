/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // signalwave palette: deep CRT background, neon cyan + magenta accents.
        ink: {
          900: "#05070d",
          800: "#0a0f1a",
          700: "#0f1626",
          600: "#161f33",
          500: "#1d2942",
        },
        cyan: {
          glow: "#22d3ee",
        },
        magenta: {
          glow: "#f472d0",
        },
      },
      fontFamily: {
        mono: [
          "JetBrains Mono",
          "Fira Code",
          "ui-monospace",
          "SFMono-Regular",
          "Menlo",
          "monospace",
        ],
      },
      boxShadow: {
        "glow-cyan": "0 0 8px rgba(34,211,238,0.6), 0 0 24px rgba(34,211,238,0.25)",
        "glow-magenta":
          "0 0 8px rgba(244,114,208,0.6), 0 0 24px rgba(244,114,208,0.25)",
      },
      keyframes: {
        scan: {
          "0%": { transform: "translateX(-100%)" },
          "100%": { transform: "translateX(100%)" },
        },
        flicker: {
          "0%, 100%": { opacity: "1" },
          "50%": { opacity: "0.85" },
        },
      },
      animation: {
        scan: "scan 4s linear infinite",
        flicker: "flicker 3s ease-in-out infinite",
      },
    },
  },
  plugins: [],
};
