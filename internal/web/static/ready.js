// Run before the body is parsed. A slow direct response must not expose a
// half-built list. Without this script the ordinary no-JS page is unchanged.
// DOMContentLoaded also releases it if the main script fails to load or run.
document.documentElement.classList.add('preparing-reader');
document.addEventListener('DOMContentLoaded', () => document.documentElement.classList.remove('preparing-reader'), { once: true });
