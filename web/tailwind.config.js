/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./web/templates/**/*.html", "./web/static/*.js"],
  darkMode: "class",
  theme: {
    extend: {
      // The neutral gray scale is remapped to CSS variables (see
      // web/input.css) instead of Tailwind's static defaults, so the
      // light/dark theme toggle works by swapping variable values under
      // [data-theme="light"] rather than requiring every bg-gray-*/
      // text-gray-*/border-gray-* utility in every template to grow a
      // dark: variant. Light mode uses the same shade *names* with an
      // inverted lightness mapping (gray-950 = near-black in dark mode,
      // near-white in light mode), so existing class names keep working
      // unchanged in both themes.
      colors: {
        gray: {
          50: "rgb(var(--gray-50) / <alpha-value>)",
          100: "rgb(var(--gray-100) / <alpha-value>)",
          200: "rgb(var(--gray-200) / <alpha-value>)",
          300: "rgb(var(--gray-300) / <alpha-value>)",
          400: "rgb(var(--gray-400) / <alpha-value>)",
          500: "rgb(var(--gray-500) / <alpha-value>)",
          600: "rgb(var(--gray-600) / <alpha-value>)",
          700: "rgb(var(--gray-700) / <alpha-value>)",
          800: "rgb(var(--gray-800) / <alpha-value>)",
          900: "rgb(var(--gray-900) / <alpha-value>)",
          950: "rgb(var(--gray-950) / <alpha-value>)",
        },
        // Semantic tokens matching the color convention in docs/web-ui.md —
        // templates reference these names, never raw hex values, so the
        // convention stays defined in exactly one place. Absolute (not
        // theme-dependent) — they're already legible on both a dark and a
        // light page background at the opacities used throughout the app.
        brand: "#0395E4",
        state: {
          running: "#22c55e",
          warning: "#d97706",
          stopped: "#dc2626",
          created: "#3b82f6",
          // Solid (not translucent) dark gray for the "paused" badge
          // background — fixed across themes, unlike the gray-* scale
          // which inverts in light mode. Paused text reuses state-warning.
          pausedbg: "#374151",
        },
        chip: {
          current: "#bbf7d0",
          "current-text": "#065f46",
          previous: "#e5e7eb",
          "previous-text": "#374151",
        },
        badge: {
          update: "#2563eb",
          available: "#f97316",
          scheduled: "#7c3aed",
        },
        // Active sidebar nav item text — a distinct orange from
        // badge.available (visually close but not the same value; kept
        // separate rather than approximated to it).
        nav: {
          active: "rgb(254 116 31)",
        },
      },
    },
  },
  plugins: [],
};
