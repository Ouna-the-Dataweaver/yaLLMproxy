/**
 * Theme Manager - Handles dark/light mode toggle with localStorage persistence
 */
const ThemeManager = {
  STORAGE_KEY: 'yallmp-theme',

  init() {
    // Apply saved theme or default to light
    const saved = localStorage.getItem(this.STORAGE_KEY) || 'light';
    this.setTheme(saved);
  },

  setTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme);
    localStorage.setItem(this.STORAGE_KEY, theme);
  },

  toggle() {
    const current = document.documentElement.getAttribute('data-theme');
    const next = current === 'dark' ? 'light' : 'dark';
    this.setTheme(next);
    return next;
  },

  getCurrent() {
    return document.documentElement.getAttribute('data-theme');
  }
};

/**
 * Initialize theme on page load
 */
function initTheme() {
  ThemeManager.init();
}

/**
 * Toggle theme
 */
function themeToggle() {
  ThemeManager.toggle();
}

/**
 * Update theme toggle button icons - kept for compatibility
 * CSS now handles icon visibility automatically
 */
function updateThemeIcons(theme) {
  // Icons are now controlled by CSS via [data-theme] selectors
  // This function is kept for backwards compatibility
}

// Auto-initialize on DOM ready
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', initTheme);
} else {
  initTheme();
}
