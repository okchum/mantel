// Lint for the page's modules (CI runs it with npx; nothing is installed).
// It catches what splitting app.js into modules risks: a name used in one
// module but not imported there throws only when that code path runs.
const browser = [
  "AbortController", "AbortSignal", "DOMException", "Element", "URLSearchParams",
  "addEventListener", "clearTimeout", "console", "document", "fetch", "getComputedStyle",
  "location", "performance", "requestAnimationFrame", "ResizeObserver", "setInterval", "setTimeout",
  "cast", // the Cast receiver SDK, loaded in receiver mode
];

export default [
  {
    files: ["**/*.js"],
    ignores: ["eslint.config.mjs"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "module",
      globals: Object.fromEntries(browser.map((n) => [n, "readonly"])),
    },
    rules: {
      "no-undef": "error",
      "no-unused-vars": ["error", { args: "none" }],
    },
  },
];
